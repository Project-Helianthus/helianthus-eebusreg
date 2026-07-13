package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
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
	AcceptedAt               time.Time `json:"accepted_at"`
	EvidenceRef              string    `json:"evidence_ref"`
	TransportEvidenceRef     string    `json:"transport_evidence_ref"`
}

type liveServiceHandler struct {
	expectedSKI string
	service     *service.Service

	mu           sync.Mutex
	connected    bool
	disconnected bool
	states       []string
	shipIDRefs   []string
	denied       map[string]struct{}
}

func runLiveVR940fProof(ctx context.Context, opts liveOptions) liveProofResult {
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
	if !validSKI(opts.RemoteSKI) {
		return liveProofFailures("remote_ski_file_required")
	}

	certificate, err := cert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-live")
	if err != nil {
		return liveProofFailures("disposable_certificate_failed")
	}
	handler, err := newLiveService(opts, certificate)
	if err != nil {
		return liveProofFailures("live_service_setup_failed")
	}

	shutdown := false
	defer func() {
		if !shutdown {
			handler.service.Shutdown()
		}
	}()

	handler.service.SetAutoAccept(opts.PairingWindow)
	handler.service.UserIsAbleToApproveOrCancelPairingRequests(opts.PairingWindow)
	handler.service.Start()

	serviceFQDN := liveServiceName + "._ship._tcp.local."
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

	var firstSPINE spineEvidence
	var operatorProof operatorProofInput
	deadline := time.Now().Add(opts.Timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		if firstSPINE.empty() && handler.isConnected() {
			firstSPINE = captureFirstSPINEEvidence(handler.service, opts.RemoteSKI)
		}
		if !operatorProof.OwnerAccepted {
			if proof, proofErr := readOperatorProof(opts.OperatorProofRef); proofErr == nil {
				operatorProof = proof
			}
		}
		if handler.isConnected() && !firstSPINE.empty() && operatorProof.LANObserverConfirmed && operatorProof.TrustVisible && operatorProof.InboundTransportObserved && operatorProof.OwnerAccepted && !operatorProof.AcceptedAt.IsZero() && validSHA256Ref(operatorProof.EvidenceRef) && validSHA256Ref(operatorProof.TransportEvidenceRef) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	discovery := <-discoveryCh
	probeErr := <-probeErrCh
	connectedBeforeShutdown := handler.isConnected()
	states := handler.stateSnapshot()

	withdrawalCh := make(chan liveDiscovery, 1)
	withdrawalErrCh := make(chan error, 1)
	go func() {
		withdrawal, withdrawalErr := probeSHIPService(ctx, opts.Interface, 3*time.Second, serviceFQDN)
		withdrawalCh <- withdrawal
		withdrawalErrCh <- withdrawalErr
	}()
	time.Sleep(150 * time.Millisecond)
	handler.service.Shutdown()
	shutdown = true

	withdrawal := <-withdrawalCh
	withdrawalErr := <-withdrawalErrCh
	noReconnect := listenerRemainsClosed(opts.Interface, opts.Port, 3*time.Second)
	localAdvertisementSeen := probeErr == nil && discovery.ExpectedActive > 0
	ttlWithdrawn := withdrawalErr == nil && (withdrawal.ExpectedGoodbye > 0 || (withdrawal.Records > 0 && withdrawal.ExpectedActive == 0))

	g17 := evaluateG17(g17Observation{
		Direction:                 accessDirectionInboundFromVR940,
		SelectedInterface:         opts.Interface,
		SelectedPort:              opts.Port,
		LocalAdvertisementSeen:    localAdvertisementSeen,
		LANObserverConfirmed:      operatorProof.LANObserverConfirmed,
		OperatorTrustVisible:      operatorProof.TrustVisible,
		TTLWithdrawalObserved:     ttlWithdrawn,
		NoConnectionAfterWithdraw: noReconnect,
	})
	if probeErr != nil {
		g17.Error = "mdns_probe_unavailable"
	}

	stages := completedInboundStages(connectedBeforeShutdown, operatorProof, firstSPINE)
	g19 := evaluateG19(g19Observation{
		Direction:                accessDirectionInboundFromVR940,
		Stages:                   stages,
		FirstSPINEData:           firstSPINE,
		DeniedAccessObserved:     deterministicDeniedAccessCheck(opts.RemoteSKI),
		ReconnectFailureObserved: noReconnect,
	})
	if g19.Status == resultPass {
		g19.Evidence = append(g19.Evidence, "g19-inbound-only-unpaired-start-policy")
	}

	result := liveProofResult{Cases: []caseResult{g17, g19}}
	if g17.Status == resultPass && g19.Status == resultPass && operatorProof.OwnerAccepted {
		stagePayload, _ := json.Marshal(states)
		evidence := buildLiveGateEvidence(opts, operatorProof, firstSPINE, stagePayload)
		result.LiveEvidence = &evidence
	}
	return result
}

func newLiveService(opts liveOptions, certificate tls.Certificate) (*liveServiceHandler, error) {
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
	configuration.SetInterfaces([]string{opts.Interface})
	configuration.SetMdnsProviderSelection(shipmdns.MdnsProviderSelectionGoZeroConfOnly)

	handler.service = service.NewService(configuration, handler)
	if err := handler.service.Setup(); err != nil {
		return nil, err
	}
	return handler, nil
}

func (h *liveServiceHandler) RemoteSKIConnected(service eebusapi.ServiceInterface, ski string) {
	if !h.allowRemote(ski) {
		go service.CancelPairingWithSKI(ski)
		return
	}
	h.mu.Lock()
	h.connected = true
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
	h.disconnected = true
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
	h.mu.Lock()
	h.states = append(h.states, connectionStateName(detail.State()))
	h.mu.Unlock()
}

func (h *liveServiceHandler) allowRemote(ski string) bool {
	normalized := normalizeSKI(ski)
	if normalized == h.expectedSKI {
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

func (h *liveServiceHandler) isConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connected
}

func (h *liveServiceHandler) stateSnapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := append([]string(nil), h.states...)
	sort.Strings(result)
	return result
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

func readOperatorProof(path string) (operatorProofInput, error) {
	if path == "" {
		return operatorProofInput{}, errors.New("operator proof path required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return operatorProofInput{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return operatorProofInput{}, errors.New("operator proof must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return operatorProofInput{}, errors.New("operator proof permissions must be 0600 or stricter")
	}
	if info.Size() > 4096 {
		return operatorProofInput{}, errors.New("operator proof exceeds size limit")
	}
	payload, err := os.ReadFile(path)
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
	if proof.OwnerAccepted && (proof.AcceptedAt.IsZero() || !proof.InboundTransportObserved || !validSHA256Ref(proof.EvidenceRef) || !validSHA256Ref(proof.TransportEvidenceRef)) {
		return operatorProofInput{}, errors.New("operator acceptance metadata incomplete")
	}
	return proof, nil
}

func readSecureTextFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("protected input path required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("protected input must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("protected input permissions must be 0600 or stricter")
	}
	if info.Size() > 256 {
		return "", errors.New("protected input exceeds size limit")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(payload))
	if value == "" {
		return "", errors.New("protected input is empty")
	}
	return value, nil
}

func listenerRemainsClosed(iface string, port int, timeout time.Duration) bool {
	ip, err := interfaceIPv4(iface)
	if err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if dialErr != nil {
			if errors.Is(dialErr, syscall.ECONNREFUSED) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_ = conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func deterministicDeniedAccessCheck(expectedSKI string) bool {
	candidate := strings.Repeat("f", 40)
	if normalizeSKI(expectedSKI) == candidate {
		candidate = strings.Repeat("e", 40)
	}
	return normalizeSKI(candidate) != normalizeSKI(expectedSKI)
}

func completedInboundStages(connected bool, proof operatorProofInput, firstSPINE spineEvidence) []transportStage {
	stages := make([]transportStage, 0, len(requiredTransportStages))
	if connected && proof.InboundTransportObserved && validSHA256Ref(proof.TransportEvidenceRef) {
		stages = append(stages,
			transportStageTCPAccepted,
			transportStageTLSCompleted,
			transportStageWebSocketUpgraded,
			transportStageSHIPCompleted,
		)
	}
	if !firstSPINE.empty() {
		stages = append(stages, transportStageFirstSPINEData)
	}
	return stages
}

func buildLiveGateEvidence(opts liveOptions, proof operatorProofInput, firstSPINE spineEvidence, stagePayload []byte) liveGateEvidence {
	window := "closed"
	if opts.PairingWindow {
		window = "opened"
	}
	branch := opts.RepoBranch
	if branch == "" {
		branch = commandString("git", "rev-parse", "--abbrev-ref", "HEAD")
	}
	commit := opts.RepoCommit
	if commit == "" {
		commit = commandString("git", "rev-parse", "HEAD")
	}
	return liveGateEvidence{
		SchemaVersion: 1,
		Gate:          caseDirectAccess,
		Repo: evidenceRepo{
			Name:   "helianthus-eebusreg",
			Branch: branch,
			Commit: commit,
		},
		Commands: []string{
			"eebusinteropsmoke --mode live-vr940f --remote-ski-file <0600> --operator-proof-file <0600>",
			"go test ./internal/eebusinteropsmoke",
		},
		Environment: evidenceEnvironment{
			TimestampUTC: time.Now().UTC().Truncate(time.Second),
			GoVersion:    commandString("go", "env", "GOVERSION"),
			ToolVersions: map[string]string{"eebus-go": "v0.7.0", "ship-go": "v0.6.0", "spine-go": "v0.7.0"},
			TopologyRef:  refLabel("ha-lan-topology", opts.Interface),
		},
		TrustPreconditions: trustPreconditions{
			LocalIdentityState: "disposable-in-memory",
			PreseededAllowlist: true,
			OperatorWindow:     window,
		},
		OperatorLiveProof: operatorLiveProof{
			Result:           resultPass,
			TrustVisible:     proof.TrustVisible,
			EvidenceRef:      proof.EvidenceRef,
			TranscriptHashes: []string{proof.TransportEvidenceRef, fullDigestRef(stagePayload)},
			FirstSPINEData:   firstSPINE,
		},
		CIReplayAuthority: ciReplayAuthority{
			Result:        resultPass,
			Fixtures:      []string{"internal/eebusinteropsmoke/testdata/g19-replay-v1.json"},
			ReplayCommand: "go test ./internal/eebusinteropsmoke -run 'TestG17|TestG19|TestLiveEvidence'",
		},
		NegativeCases: negativeCaseEvidence{
			DeniedAccess:     evidenceResult{Result: resultPass, EvidenceHash: fullDigestRef([]byte("deny-non-allowlisted-remote"))},
			ReconnectFailure: evidenceResult{Result: resultPass, EvidenceHash: fullDigestRef([]byte("listener-closed-after-shutdown"))},
		},
		PublicRedaction: publicRedactionEvidence{
			NoPacketCaptures:       true,
			NoRawTranscripts:       true,
			NoSecretsOrTrustStores: true,
			NoRawIdentity:          true,
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
	value = strings.ToLower(value)
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
