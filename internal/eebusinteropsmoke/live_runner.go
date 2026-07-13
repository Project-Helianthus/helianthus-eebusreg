package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	eebusapi "github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	shipmdns "github.com/enbility/ship-go/mdns"
	"github.com/enbility/spine-go/model"
	"golang.org/x/sys/unix"
)

const liveServiceName = "Helianthus EnergyManagementSystem RawProbe"

type liveProofResult struct {
	Cases        []caseResult
	LiveEvidence *liveGateEvidence
}

type operatorProofInput struct {
	LANObserverConfirmed     bool      `json:"lan_observer_confirmed"`
	TrustVisible             bool      `json:"trust_visible"`
	InboundTransportObserved bool      `json:"inbound_transport_observed"`
	OwnerAccepted            bool      `json:"owner_accepted"`
	RunNonce                 string    `json:"run_nonce"`
	RunRef                   string    `json:"run_ref"`
	ChallengeRef             string    `json:"challenge_ref"`
	ExpectedRemoteDigest     string    `json:"expected_remote_digest"`
	InterfaceRef             string    `json:"interface_ref"`
	PortRef                  string    `json:"port_ref"`
	ConnectionGenerationRef  string    `json:"connection_generation_ref"`
	ChallengeIssuedAt        time.Time `json:"challenge_issued_at"`
	FirstSPINECapturedAt     time.Time `json:"first_spine_captured_at"`
	RunStartedAt             time.Time `json:"run_started_at"`
	RunExpiresAt             time.Time `json:"run_expires_at"`
	ObservedAt               time.Time `json:"observed_at"`
	AcceptedAt               time.Time `json:"accepted_at"`
	EvidenceRef              string    `json:"evidence_ref"`
	TransportHash            string    `json:"transport_hash"`
	FirstSPINEHash           string    `json:"first_spine_hash"`
}

type liveRunBinding struct {
	nonce                string
	key                  []byte
	runNonceRef          string
	runRef               string
	expectedRemoteDigest string
	interfaceRef         string
	portRef              string
	startedAt            time.Time
	expiresAt            time.Time
	challengeIssuedAt    time.Time
}

type operatorChallenge struct {
	Kind                    string    `json:"kind"`
	RunNonce                string    `json:"run_nonce"`
	RunRef                  string    `json:"run_ref"`
	ChallengeRef            string    `json:"challenge_ref,omitempty"`
	ExpectedRemoteDigest    string    `json:"expected_remote_digest"`
	InterfaceRef            string    `json:"interface_ref"`
	PortRef                 string    `json:"port_ref"`
	ConnectionGenerationRef string    `json:"connection_generation_ref,omitempty"`
	ChallengeIssuedAt       time.Time `json:"challenge_issued_at,omitempty"`
	FirstSPINECapturedAt    time.Time `json:"first_spine_captured_at,omitempty"`
	RunStartedAt            time.Time `json:"run_started_at"`
	RunExpiresAt            time.Time `json:"run_expires_at"`
	TransportHash           string    `json:"transport_hash,omitempty"`
	FirstPostAccessSPINE    string    `json:"first_post_access_spine_hash,omitempty"`
}

type connectionSnapshot struct {
	Connected   bool
	Generation  uint64
	ConnectedAt time.Time
}

type spineCapture struct {
	Evidence   spineEvidence
	Generation uint64
	CapturedAt time.Time
}

type liveServiceHandler struct {
	expectedSKI string
	service     *service.Service

	mu               sync.Mutex
	expectedApproved bool
	connected        bool
	generation       uint64
	connectedAt      time.Time
	states           []string
	shipIDRefs       []string
	denied           map[string]struct{}
}

func runLiveVR940fProof(ctx context.Context, opts liveOptions) liveProofResult {
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.Port == 0 {
		opts.Port = 4712
	}
	if opts.Interface == "" {
		opts.Interface = defaultLANInterface()
	}
	if opts.Interface == "" {
		return liveProofFailures("selected_interface_required")
	}
	opts.Interface = strings.TrimSpace(opts.Interface)
	if !validSKI(opts.RemoteSKI) {
		return liveProofFailures("remote_ski_file_required")
	}
	opts.RemoteSKI = normalizeSKI(opts.RemoteSKI)
	binding, err := newLiveRunBinding(opts, opts.RemoteSKI, time.Now())
	if err != nil {
		return liveProofFailures("run_binding_generation_failed")
	}
	if err := emitOperatorChallenge(opts.ChallengeWriter, binding.operatorChallenge("", spineCapture{})); err != nil {
		return liveProofFailures("operator_challenge_write_failed")
	}

	certificate, err := cert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-live")
	if err != nil {
		return liveProofFailures("disposable_certificate_failed")
	}
	handler, err := newLiveService(opts, certificate)
	if err != nil {
		return liveProofFailures("live_service_setup_failed")
	}
	if err := handler.approveExpectedRemote(); err != nil {
		return liveProofFailures("expected_remote_approval_failed")
	}
	handler.service.Start()
	publisher, err := startLANSHIPPublisher(
		opts.Interface,
		opts.Port,
		handler.service.LocalService().SKI(),
		handler.service.LocalService().ShipID(),
		opts.PairingWindow,
	)
	if err != nil {
		handler.service.Shutdown()
		return liveProofFailures("mdns_probe_unavailable")
	}
	shutdown := false
	shutdownAll := func() {
		if shutdown {
			return
		}
		publisher.shutdown()
		handler.service.Shutdown()
		shutdown = true
	}
	defer shutdownAll()

	serviceFQDN := publisher.serviceFQDN
	probeTimeout := 5 * time.Second
	if opts.Timeout < probeTimeout {
		probeTimeout = opts.Timeout
	}
	discoveryCh := make(chan liveDiscovery, 1)
	probeErrCh := make(chan error, 1)
	go func() {
		discovery, probeErr := probeSHIPService(ctx, opts.Interface, probeTimeout, serviceFQDN)
		discoveryCh <- discovery
		probeErrCh <- probeErr
	}()

	var firstSPINE spineCapture
	var operatorProof operatorProofInput
	var emittedChallengeInputs string
	deadline := binding.expiresAt
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		snapshot := handler.connectionSnapshot()
		if snapshot.Connected && (firstSPINE.Generation != snapshot.Generation || firstSPINE.Evidence.empty()) {
			candidate := captureFirstSPINEEvidence(handler.service, opts.RemoteSKI)
			current := handler.connectionSnapshot()
			if !candidate.empty() && current.Connected && current.Generation == snapshot.Generation {
				firstSPINE = spineCapture{Evidence: candidate, Generation: current.Generation, CapturedAt: time.Now().UTC()}
			}
		}
		if proof, proofErr := readOperatorProof(opts.OperatorProofRef); proofErr == nil {
			operatorProof = proof
		}
		if !firstSPINE.Evidence.empty() && validSHA256Ref(operatorProof.TransportHash) {
			challengeInputs := strings.Join([]string{operatorProof.TransportHash, firstSPINE.Evidence.dataHash(), strconv.FormatUint(firstSPINE.Generation, 10)}, "\x00")
			if challengeInputs != emittedChallengeInputs {
				binding.challengeIssuedAt = time.Now().UTC()
				if err := emitOperatorChallenge(opts.ChallengeWriter, binding.operatorChallenge(operatorProof.TransportHash, firstSPINE)); err != nil {
					return liveProofFailures("operator_challenge_write_failed")
				}
				emittedChallengeInputs = challengeInputs
			}
		}
		if validateOperatorProof(operatorProof, binding, firstSPINE, snapshot, time.Now()) == nil {
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(250 * time.Millisecond):
		}
	}

	discovery := <-discoveryCh
	probeErr := <-probeErrCh
	connectionBeforeShutdown := handler.connectionSnapshot()
	states := handler.stateSnapshot()
	proofErr := validateOperatorProof(operatorProof, binding, firstSPINE, connectionBeforeShutdown, time.Now())
	withdrawal, withdrawalErr := observeSHIPWithdrawal(ctx, opts.Interface, 3*time.Second, serviceFQDN, shutdownAll)
	shutdownAll()
	noReconnect := listenerRemainsClosed(opts.Interface, opts.Port, 3*time.Second)
	localAdvertisementSeen := probeErr == nil && discovery.ExpectedActive > 0
	ttlWithdrawn := withdrawalErr == nil && withdrawal.ExpectedGoodbye > 0
	proofValid := proofErr == nil

	g17 := evaluateG17(g17Observation{
		Direction:                 accessDirectionInboundFromVR940,
		SelectedInterface:         opts.Interface,
		SelectedPort:              opts.Port,
		LocalAdvertisementSeen:    localAdvertisementSeen,
		LANObserverConfirmed:      proofValid && operatorProof.LANObserverConfirmed,
		OperatorTrustVisible:      proofValid && operatorProof.TrustVisible,
		TTLWithdrawalObserved:     ttlWithdrawn,
		NoConnectionAfterWithdraw: noReconnect,
	})
	if probeErr != nil {
		g17.Error = "mdns_probe_unavailable"
	} else if withdrawalErr != nil {
		g17.Error = "mdns_withdrawal_probe_unavailable"
	}

	deniedReplay, reconnectReplay, replayHash, replayErr := replayNegativeObservations()
	if replayErr != nil {
		deniedReplay = negativeObservation{}
		reconnectReplay = negativeObservation{}
	}
	stages := completedInboundStages(connectionBeforeShutdown, operatorProof, firstSPINE, binding)
	g19 := evaluateG19(g19Observation{
		Direction:            accessDirectionInboundFromVR940,
		Stages:               stages,
		CurrentConnection:    proofValid && connectionBeforeShutdown.Connected,
		ConnectionGeneration: connectionBeforeShutdown.Generation,
		FirstSPINEGeneration: firstSPINE.Generation,
		FirstSPINEData:       firstSPINE.Evidence,
		DeniedAccess:         deniedReplay,
		ReconnectFailure:     reconnectReplay,
	})

	result := liveProofResult{Cases: []caseResult{g17, g19}}
	if g17.Status == resultPass && g19.Status == resultPass && proofValid && replayErr == nil {
		stagePayload, marshalErr := json.Marshal(states)
		if marshalErr != nil {
			return liveProofFailures("stage_evidence_encoding_failed")
		}
		evidence := buildLiveGateEvidence(opts, binding, operatorProof, firstSPINE, stagePayload, g19, replayHash)
		result.LiveEvidence = &evidence
	}
	return result
}

func newLiveService(opts liveOptions, certificate tls.Certificate) (*liveServiceHandler, error) {
	loopback := defaultLoopbackInterface()
	if loopback == "" {
		return nil, errors.New("loopback interface required for eebus discovery isolation")
	}
	handler := &liveServiceHandler{
		expectedSKI: normalizeSKI(opts.RemoteSKI),
		denied:      make(map[string]struct{}),
	}
	configuration, err := eebusapi.NewConfiguration(
		"Helianthus",
		"Helianthus",
		"EnergyManagementSystem",
		"RawProbe",
		model.DeviceTypeTypeEnergyManagementSystem,
		[]model.EntityTypeType{model.EntityTypeTypeCEM},
		opts.Port,
		certificate,
		2*time.Second,
	)
	if err != nil {
		return nil, err
	}
	configuration.SetAlternateIdentifier("Helianthus-EnergyManagementSystem-RawProbe")
	configuration.SetAlternateMdnsServiceName(liveServiceName)
	configuration.SetInterfaces([]string{loopback})
	configuration.SetMdnsProviderSelection(shipmdns.MdnsProviderSelectionGoZeroConfOnly)

	handler.service = service.NewService(configuration, handler)
	if err := handler.service.Setup(); err != nil {
		return nil, err
	}
	return handler, nil
}

func (h *liveServiceHandler) approveExpectedRemote() error {
	if h == nil || h.service == nil || !validSKI(h.expectedSKI) {
		return errors.New("expected remote approval configuration invalid")
	}
	h.service.SetAutoAccept(false)
	h.service.UserIsAbleToApproveOrCancelPairingRequests(false)
	h.service.RegisterRemoteSKI(h.expectedSKI)
	remote := h.service.RemoteServiceForSKI(h.expectedSKI)
	if remote == nil || !remote.Trusted() || h.service.IsAutoAcceptEnabled() {
		return errors.New("expected remote approval was not installed before start")
	}
	h.mu.Lock()
	h.expectedApproved = true
	h.mu.Unlock()
	return nil
}

func (h *liveServiceHandler) RemoteSKIConnected(service eebusapi.ServiceInterface, ski string) {
	if !h.allowRemote(ski) {
		go service.CancelPairingWithSKI(ski)
		return
	}
	h.mu.Lock()
	if !h.expectedApproved {
		h.mu.Unlock()
		go service.CancelPairingWithSKI(ski)
		return
	}
	h.generation++
	h.connected = true
	h.connectedAt = time.Now().UTC()
	h.states = append(h.states, connectionStateName(shipapi.ConnectionStateCompleted))
	h.mu.Unlock()
	service.SetAutoAccept(false)
	service.UserIsAbleToApproveOrCancelPairingRequests(false)
}

func (h *liveServiceHandler) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {
	if normalizeSKI(ski) != h.expectedSKI {
		return
	}
	h.mu.Lock()
	h.connected = false
	h.states = append(h.states, "disconnected")
	h.mu.Unlock()
}

func (h *liveServiceHandler) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, _ []shipapi.RemoteService) {
}

func (h *liveServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {
	if normalizeSKI(ski) != h.expectedSKI {
		return
	}
	h.mu.Lock()
	h.shipIDRefs = append(h.shipIDRefs, digestRef(shipID))
	h.mu.Unlock()
}

func (h *liveServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	if !h.allowRemote(ski) {
		return
	}
	if detail == nil {
		return
	}
	h.mu.Lock()
	h.states = append(h.states, connectionStateName(detail.State()))
	h.mu.Unlock()
}

func (h *liveServiceHandler) allowRemote(ski string) bool {
	normalized := normalizeSKI(ski)
	if normalized != "" && normalized == h.expectedSKI {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.denied[normalized]; exists {
		return false
	}
	h.denied[normalized] = struct{}{}
	return false
}

func (h *liveServiceHandler) connectionSnapshot() connectionSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return connectionSnapshot{Connected: h.connected, Generation: h.generation, ConnectedAt: h.connectedAt}
}

func (h *liveServiceHandler) stateSnapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return sortedUnique(h.states)
}

func captureFirstSPINEEvidence(svc *service.Service, remoteSKI string) spineEvidence {
	remote := svc.LocalDevice().RemoteDeviceForSki(remoteSKI)
	if remote == nil {
		return spineEvidence{}
	}
	evidence := spineEvidence{}
	for _, entity := range remote.Entities() {
		evidence.EntityTypes = append(evidence.EntityTypes, fmt.Sprint(entity.EntityType()))
		for _, feature := range entity.Features() {
			evidence.FeatureTypes = append(evidence.FeatureTypes, fmt.Sprintf("%s/%s", feature.Type(), feature.Role()))
		}
	}
	for _, useCase := range remote.UseCases() {
		payload, err := json.Marshal(useCase)
		if err == nil {
			evidence.UseCaseRefs = append(evidence.UseCaseRefs, "usecase-"+fullDigestRef(payload))
		}
	}
	return evidence.normalized()
}

func newLiveRunBinding(opts liveOptions, expectedSKI string, now time.Time) (liveRunBinding, error) {
	nonce := make([]byte, 32)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return liveRunBinding{}, err
	}
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return liveRunBinding{}, err
	}
	startedAt := now.UTC()
	expiresAt := startedAt.Add(opts.Timeout)
	nonceText := hex.EncodeToString(nonce)
	binding := liveRunBinding{
		nonce:       nonceText,
		key:         key,
		runNonceRef: fullDigestRef(nonce),
		startedAt:   startedAt,
		expiresAt:   expiresAt,
	}
	binding.runRef = fullDigestRef([]byte(strings.Join([]string{binding.runNonceRef, startedAt.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano)}, "\x00")))
	binding.expectedRemoteDigest = keyedDigestRef(key, []byte("expected-remote\x00"+normalizeSKI(expectedSKI)))
	binding.interfaceRef = keyedDigestRef(key, []byte("interface\x00"+strings.TrimSpace(opts.Interface)))
	binding.portRef = keyedDigestRef(key, []byte("port\x00"+strconv.Itoa(opts.Port)))
	return binding, nil
}

func (b liveRunBinding) generationRef(generation uint64) string {
	return keyedDigestRef(b.key, []byte("connection-generation\x00"+strconv.FormatUint(generation, 10)))
}

func (b liveRunBinding) challenge(transportHash string, firstSPINE spineCapture) string {
	parts := []string{
		b.runNonceRef,
		b.runRef,
		b.expectedRemoteDigest,
		b.interfaceRef,
		b.portRef,
		b.startedAt.Format(time.RFC3339Nano),
		b.expiresAt.Format(time.RFC3339Nano),
		b.challengeIssuedAt.Format(time.RFC3339Nano),
		strings.TrimSpace(transportHash),
		firstSPINE.Evidence.dataHash(),
		firstSPINE.CapturedAt.UTC().Format(time.RFC3339Nano),
		b.generationRef(firstSPINE.Generation),
	}
	return keyedDigestRef(b.key, []byte(strings.Join(parts, "\x00")))
}

func (b liveRunBinding) operatorChallenge(transportHash string, firstSPINE spineCapture) operatorChallenge {
	challenge := operatorChallenge{
		Kind:                 "helianthus-eebus-live-proof",
		RunNonce:             b.nonce,
		RunRef:               b.runRef,
		ExpectedRemoteDigest: b.expectedRemoteDigest,
		InterfaceRef:         b.interfaceRef,
		PortRef:              b.portRef,
		RunStartedAt:         b.startedAt,
		RunExpiresAt:         b.expiresAt,
		TransportHash:        strings.TrimSpace(transportHash),
		FirstPostAccessSPINE: firstSPINE.Evidence.dataHash(),
	}
	if firstSPINE.Generation != 0 && !firstSPINE.Evidence.empty() && !firstSPINE.CapturedAt.IsZero() {
		challenge.ConnectionGenerationRef = b.generationRef(firstSPINE.Generation)
		challenge.ChallengeIssuedAt = b.challengeIssuedAt
		challenge.FirstSPINECapturedAt = firstSPINE.CapturedAt.UTC()
	} else {
		challenge.FirstPostAccessSPINE = ""
	}
	if challenge.TransportHash != "" && challenge.FirstPostAccessSPINE != "" && challenge.ConnectionGenerationRef != "" {
		challenge.ChallengeRef = b.challenge(challenge.TransportHash, firstSPINE)
	}
	return challenge
}

func emitOperatorChallenge(writer io.Writer, challenge operatorChallenge) error {
	if writer == nil {
		writer = os.Stderr
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(challenge); err != nil {
		return fmt.Errorf("operator challenge write failed: %w", err)
	}
	return nil
}

func validateOperatorProof(proof operatorProofInput, binding liveRunBinding, firstSPINE spineCapture, connection connectionSnapshot, now time.Time) error {
	proof = proof.normalized()
	if !proof.LANObserverConfirmed || !proof.TrustVisible || !proof.InboundTransportObserved || !proof.OwnerAccepted {
		return errors.New("operator confirmations incomplete")
	}
	if proof.RunNonce != binding.nonce || proof.RunRef != binding.runRef || proof.ExpectedRemoteDigest != binding.expectedRemoteDigest || proof.InterfaceRef != binding.interfaceRef || proof.PortRef != binding.portRef {
		return errors.New("operator proof run binding mismatch")
	}
	if !proof.RunStartedAt.Equal(binding.startedAt) || !proof.RunExpiresAt.Equal(binding.expiresAt) {
		return errors.New("operator proof run window mismatch")
	}
	if binding.challengeIssuedAt.IsZero() || firstSPINE.CapturedAt.IsZero() || connection.ConnectedAt.IsZero() || proof.ObservedAt.IsZero() || proof.AcceptedAt.IsZero() || proof.ObservedAt.Before(binding.startedAt) || proof.ObservedAt.After(binding.challengeIssuedAt) || proof.AcceptedAt.Before(binding.challengeIssuedAt) || proof.AcceptedAt.Before(proof.ObservedAt) || proof.AcceptedAt.After(binding.expiresAt) || proof.AcceptedAt.After(now.UTC().Add(30*time.Second)) || firstSPINE.CapturedAt.Before(connection.ConnectedAt) || firstSPINE.CapturedAt.After(binding.challengeIssuedAt) {
		return errors.New("operator proof timestamps invalid")
	}
	if !proof.ChallengeIssuedAt.Equal(binding.challengeIssuedAt) || !proof.FirstSPINECapturedAt.Equal(firstSPINE.CapturedAt.UTC()) {
		return errors.New("operator proof timestamp binding mismatch")
	}
	if !validSHA256Ref(proof.EvidenceRef) || !validSHA256Ref(proof.TransportHash) {
		return errors.New("operator proof integrity hashes invalid")
	}
	if !connection.Connected || connection.Generation == 0 || firstSPINE.Generation != connection.Generation || firstSPINE.Evidence.empty() {
		return errors.New("operator proof is not bound to the current connection generation")
	}
	firstSPINEHash := firstSPINE.Evidence.dataHash()
	if proof.ConnectionGenerationRef != binding.generationRef(connection.Generation) || proof.FirstSPINEHash != firstSPINEHash || proof.ChallengeRef != binding.challenge(proof.TransportHash, firstSPINE) {
		return errors.New("operator proof challenge mismatch")
	}
	return nil
}

func (p operatorProofInput) normalized() operatorProofInput {
	p.RunNonce = strings.TrimSpace(p.RunNonce)
	p.RunRef = strings.TrimSpace(p.RunRef)
	p.ChallengeRef = strings.TrimSpace(p.ChallengeRef)
	p.ExpectedRemoteDigest = strings.TrimSpace(p.ExpectedRemoteDigest)
	p.InterfaceRef = strings.TrimSpace(p.InterfaceRef)
	p.PortRef = strings.TrimSpace(p.PortRef)
	p.ConnectionGenerationRef = strings.TrimSpace(p.ConnectionGenerationRef)
	p.EvidenceRef = strings.TrimSpace(p.EvidenceRef)
	p.TransportHash = strings.TrimSpace(p.TransportHash)
	p.FirstSPINEHash = strings.TrimSpace(p.FirstSPINEHash)
	return p
}

func readOperatorProof(path string) (operatorProofInput, error) {
	payload, err := readProtectedFile(path, "operator proof", 4096)
	if err != nil {
		return operatorProofInput{}, err
	}
	var proof operatorProofInput
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return operatorProofInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return operatorProofInput{}, errors.New("operator proof contains trailing data")
	}
	return proof.normalized(), nil
}

func readSecureTextFile(path string) (string, error) {
	payload, err := readProtectedFile(path, "protected input", 256)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(payload))
	if value == "" {
		return "", errors.New("protected input is empty")
	}
	return value, nil
}

func readProtectedFile(path, label string, maxSize int64) (payload []byte, resultErr error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%s path required", label)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s must be a regular non-symlink file", label)
		}
		return nil, fmt.Errorf("%s unavailable", label)
	}
	file := os.NewFile(uintptr(fd), label)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%s unavailable", label)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%s close failed", label))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s metadata unavailable", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular non-symlink file", label)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s permissions must be 0600 or stricter", label)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s exceeds size limit", label)
	}
	payload, err = io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s read failed", label)
	}
	if int64(len(payload)) > maxSize {
		return nil, fmt.Errorf("%s exceeds size limit", label)
	}
	return payload, nil
}

func listenerRemainsClosed(iface string, port int, timeout time.Duration) bool {
	ip, err := interfaceIPv4(iface)
	if err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
	attempts := 0
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if dialErr != nil {
			if !errors.Is(dialErr, syscall.ECONNREFUSED) {
				return false
			}
			attempts++
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if closeErr := conn.Close(); closeErr != nil {
			return false
		}
		return false
	}
	return attempts > 0
}

func completedInboundStages(connection connectionSnapshot, proof operatorProofInput, firstSPINE spineCapture, binding liveRunBinding) []transportStage {
	stages := make([]transportStage, 0, len(requiredTransportStages))
	if validateOperatorProof(proof, binding, firstSPINE, connection, time.Now()) == nil {
		stages = append(stages,
			transportStageTCPAccepted,
			transportStageTLSCompleted,
			transportStageWebSocketUpgraded,
			transportStageSHIPCompleted,
		)
		stages = append(stages, transportStageFirstSPINEData)
	}
	return stages
}

func buildLiveGateEvidence(opts liveOptions, binding liveRunBinding, proof operatorProofInput, firstSPINE spineCapture, stagePayload []byte, g19 caseResult, replayHash string) liveGateEvidence {
	window := "closed"
	if opts.PairingWindow {
		window = "opened"
	}
	repo := currentRepoEvidence()
	proof = proof.normalized()
	return liveGateEvidence{
		SchemaVersion: 1,
		Gate:          caseDirectAccess,
		CaseBinding: liveCaseBinding{
			ID:         g19.ID,
			Status:     g19.Status,
			ResultHash: g19.dataHash(),
		},
		Repo: repo,
		Commands: []string{
			"eebusinteropsmoke --mode live-vr940f --interface <selected-lan> --port <selected-port> --timeout <bounded> --pairing-window --remote-ski-file <protected-0600> --operator-proof-file <protected-0600>",
			"go test ./internal/eebusinteropsmoke",
		},
		Environment: evidenceEnvironment{
			TimestampUTC: proof.AcceptedAt.UTC(),
			GoVersion:    commandString("go", "env", "GOVERSION"),
			ToolVersions: map[string]string{"eebus-go": "v0.7.0", "ship-go": "v0.6.0", "spine-go": "v0.7.0"},
			TopologyRef:  binding.interfaceRef,
		},
		TrustPreconditions: trustPreconditions{
			LocalIdentityState:     "disposable-in-memory",
			ExpectedRemoteApproved: true,
			AutoAcceptEnabled:      false,
			DiscoveryIsolation:     "loopback",
			OperatorWindow:         window,
		},
		OperatorLiveProof: operatorLiveProof{
			Result:                  resultPass,
			TrustVisible:            proof.TrustVisible,
			RunNonceRef:             binding.runNonceRef,
			RunRef:                  binding.runRef,
			ChallengeRef:            proof.ChallengeRef,
			ExpectedRemoteDigest:    binding.expectedRemoteDigest,
			InterfaceRef:            binding.interfaceRef,
			PortRef:                 binding.portRef,
			ConnectionGenerationRef: proof.ConnectionGenerationRef,
			ChallengeIssuedAt:       binding.challengeIssuedAt,
			FirstSPINECapturedAt:    firstSPINE.CapturedAt.UTC(),
			RunStartedAt:            binding.startedAt,
			RunExpiresAt:            binding.expiresAt,
			ObservedAt:              proof.ObservedAt.UTC(),
			AcceptedAt:              proof.AcceptedAt.UTC(),
			EvidenceRef:             proof.EvidenceRef,
			TransportHash:           proof.TransportHash,
			TranscriptHashes:        []string{proof.TransportHash, fullDigestRef(stagePayload)},
			FirstSPINEData:          firstSPINE.Evidence,
			FirstSPINEDataHash:      firstSPINE.Evidence.dataHash(),
		},
		CIReplayAuthority: ciReplayAuthority{
			Result:        resultPass,
			Fixtures:      []replayArtifact{{Path: "internal/eebusinteropsmoke/testdata/g19-replay-v1.json", SHA256: replayHash}},
			ReplayCommand: "go test ./internal/eebusinteropsmoke -run 'TestG17|TestG19|TestLiveEvidence'",
		},
		NegativeCases: negativeCaseEvidence{
			DeniedAccess: evidenceResult{
				Result:       resultPass,
				Authority:    negativeAuthorityCIReplay,
				LiveObserved: false,
				EvidenceHash: replayHash,
			},
			ReconnectFailure: evidenceResult{
				Result:       resultPass,
				Authority:    negativeAuthorityCIReplay,
				LiveObserved: false,
				EvidenceHash: replayHash,
			},
		},
		PublicRedaction: publicRedactionEvidence{
			NoPacketCaptures:    true,
			NoRawTranscripts:    true,
			NoSensitiveMaterial: true,
			NoRawIdentity:       true,
		},
		OwnerAcceptance: ownerAcceptance{
			Accepted:   proof.OwnerAccepted,
			AcceptedAt: proof.AcceptedAt.UTC().Truncate(time.Second),
			Notes:      "accepted from redacted operator observation",
		},
	}
}

func liveProofFailures(code string) liveProofResult {
	return liveProofResult{Cases: []caseResult{
		{ID: caseLive, Status: resultFail, Evidence: []string{"g17-live-runner-failed-closed"}, Error: code},
		{ID: caseDirectAccess, Status: resultFail, Evidence: []string{"g19-live-runner-failed-closed"}, Error: code},
	}}
}

func normalizeSKI(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer(":", "", "-", "", " ", "").Replace(value)
}

func validSKI(value string) bool {
	normalized := normalizeSKI(value)
	if len(normalized) != 40 {
		return false
	}
	_, err := hex.DecodeString(normalized)
	return err == nil
}
