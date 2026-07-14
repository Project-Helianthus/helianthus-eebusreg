package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	shiphub "github.com/enbility/ship-go/hub"
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
	FirstSPINEPayloadHash    string    `json:"first_spine_payload_hash"`
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
	FirstSPINEPayloadHash   string    `json:"first_spine_payload_hash,omitempty"`
}

type connectionSnapshot struct {
	Connected   bool
	Generation  uint64
	ConnectedAt time.Time
}

type spineCapture struct {
	Evidence    spineEvidence
	PayloadHash string
	Generation  uint64
	CapturedAt  time.Time
}

type liveServiceHandler struct {
	expectedSKI string
	service     *service.Service
	hub         shipapi.HubInterface
	server      *instrumentedSHIPServer

	mu               sync.Mutex
	expectedApproved bool
	connected        bool
	generation       uint64
	connectedAt      time.Time
	firstSPINE       spineCapture
	states           []string
	shipIDRefs       []string
	denied           map[string]struct{}
	withdrawal       *postWithdrawalTracker
}

type liveProofDependencies struct {
	startService func(*liveServiceHandler) error
}

func runLiveVR940fProof(ctx context.Context, opts liveOptions) liveProofResult {
	return runLiveVR940fProofWithDependencies(ctx, opts, liveProofDependencies{
		startService: func(handler *liveServiceHandler) error {
			return handler.start()
		},
	})
}

func runLiveVR940fProofWithDependencies(ctx context.Context, opts liveOptions, dependencies liveProofDependencies) liveProofResult {
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
	if dependencies.startService == nil || dependencies.startService(handler) != nil {
		return liveProofFailures("live_service_start_failed")
	}
	publisher, err := startLANSHIPPublisher(
		opts.Interface,
		opts.Port,
		handler.service.LocalService().SKI(),
		handler.service.LocalService().ShipID(),
		opts.PairingWindow,
	)
	if err != nil {
		handler.shutdown()
		return liveProofFailures("mdns_probe_unavailable")
	}
	shutdown := false
	shutdownAll := func() {
		if shutdown {
			return
		}
		publisher.shutdown()
		handler.shutdown()
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
		firstSPINE = handler.firstSPINESnapshot()
		if proof, proofErr := readOperatorProof(opts.OperatorProofRef); proofErr == nil {
			operatorProof = proof
		}
		if !firstSPINE.Evidence.empty() && validSHA256Ref(firstSPINE.PayloadHash) && validSHA256Ref(operatorProof.TransportHash) {
			challengeInputs := strings.Join([]string{operatorProof.TransportHash, firstSPINE.PayloadHash, firstSPINE.Evidence.dataHash(), strconv.FormatUint(firstSPINE.Generation, 10)}, "\x00")
			if challengeInputs != emittedChallengeInputs {
				binding.challengeIssuedAt = time.Now().UTC()
				if err := emitOperatorChallenge(opts.ChallengeWriter, binding.operatorChallenge(operatorProof.TransportHash, firstSPINE)); err != nil {
					return liveProofFailures("operator_challenge_write_failed")
				}
				emittedChallengeInputs = challengeInputs
			}
		}
		if validateG19OperatorProof(operatorProof, binding, firstSPINE, snapshot, time.Now()) == nil {
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
	firstSPINE = handler.firstSPINESnapshot()
	states := handler.stateSnapshot()
	g17ProofErr := validateG17OperatorProof(operatorProof, binding, time.Now())
	g19ProofErr := validateG19OperatorProof(operatorProof, binding, firstSPINE, connectionBeforeShutdown, time.Now())

	const negativeWindow = 3 * time.Second
	windowClock := realNegativeWindowClock{}
	windowTracker := &postWithdrawalTracker{}
	handler.setWithdrawalTracker(windowTracker)
	windowResultCh := make(chan postWithdrawalWindowResult, 1)
	windowStarted := false
	withdrawal, withdrawalErr := observeSHIPWithdrawal(ctx, opts.Interface, negativeWindow, serviceFQDN, func() error {
		if err := windowTracker.observerReady(windowClock.Now()); err != nil {
			return err
		}
		publisher.shutdown()
		if err := windowTracker.advertisementWithdrawn(windowClock.Now()); err != nil {
			return err
		}
		windowStarted = true
		go func() {
			satisfied, windowErr := waitPostWithdrawalWindow(ctx, windowTracker, negativeWindow, windowClock)
			windowResultCh <- postWithdrawalWindowResult{Satisfied: satisfied, Err: windowErr}
		}()
		return nil
	})
	postWithdrawalNegative := false
	if windowStarted {
		windowResult := <-windowResultCh
		postWithdrawalNegative = windowResult.Err == nil && windowResult.Satisfied
	}
	handler.setWithdrawalTracker(nil)
	shutdownAll()
	localAdvertisementSeen := probeErr == nil && discovery.ExpectedActive > 0
	ttlWithdrawn := withdrawalErr == nil && withdrawal.ExpectedGoodbye > 0
	g17ProofValid := g17ProofErr == nil
	g19ProofValid := g19ProofErr == nil

	g17 := evaluateG17(g17Observation{
		Direction:                 accessDirectionInboundFromVR940,
		SelectedInterface:         opts.Interface,
		SelectedPort:              opts.Port,
		LocalAdvertisementSeen:    localAdvertisementSeen,
		LANObserverConfirmed:      g17ProofValid && operatorProof.LANObserverConfirmed,
		OperatorTrustVisible:      g17ProofValid && operatorProof.TrustVisible,
		TTLWithdrawalObserved:     ttlWithdrawn,
		NoConnectionAfterWithdraw: postWithdrawalNegative,
	})
	if probeErr != nil {
		g17.Error = "mdns_probe_unavailable"
	} else if withdrawalErr != nil {
		g17.Error = "mdns_withdrawal_probe_unavailable"
	}

	deniedReplay, reconnectReplay, replayArtifact, replayErr := replayNegativeObservations()
	if replayErr != nil {
		deniedReplay = negativeObservation{}
		reconnectReplay = negativeObservation{}
	}
	stages := completedInboundStages(connectionBeforeShutdown, operatorProof, firstSPINE, binding)
	g19 := evaluateG19(g19Observation{
		Direction:             accessDirectionInboundFromVR940,
		Stages:                stages,
		CurrentConnection:     g19ProofValid && connectionBeforeShutdown.Connected,
		ConnectionGeneration:  connectionBeforeShutdown.Generation,
		FirstSPINEGeneration:  firstSPINE.Generation,
		FirstSPINEPayloadHash: firstSPINE.PayloadHash,
		FirstSPINEData:        firstSPINE.Evidence,
		DeniedAccess:          deniedReplay,
		ReconnectFailure:      reconnectReplay,
	})

	result := liveProofResult{}
	if g19.Status == resultPass && g19ProofValid && replayErr == nil {
		evidence, evidenceErr := constructCanonicalG19Evidence(opts, binding, operatorProof, firstSPINE, states, g19, deniedReplay, reconnectReplay, replayArtifact)
		if evidenceErr != nil {
			g19 = failCanonicalG19(g19)
		} else {
			result.LiveEvidence = evidence
		}
	}
	result.Cases = []caseResult{g17, g19}
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

	handler.service = service.NewService(configuration, handler)
	if err := handler.service.Setup(); err != nil {
		return nil, err
	}
	localService := handler.service.LocalService()
	discovery := &disabledInternalMDNS{}
	hubReader := &liveHubReader{delegate: handler.service, handler: handler}
	connectionHub := shiphub.NewHub(hubReader, discovery, configuration.Port(), certificate, localService)
	handler.hub = connectionHub
	handler.server = &instrumentedSHIPServer{
		port:        configuration.Port(),
		certificate: certificate,
		handler:     connectionHub,
		discovery:   discovery,
		hub:         connectionHub,
		onAccept:    handler.recordInboundAttempt,
	}
	return handler, nil
}

type disabledInternalMDNS struct{}

func (*disabledInternalMDNS) Start(shipapi.MdnsReportInterface) error { return nil }
func (*disabledInternalMDNS) Shutdown()                               {}
func (*disabledInternalMDNS) AnnounceMdnsEntry() error                { return nil }
func (*disabledInternalMDNS) UnannounceMdnsEntry()                    {}
func (*disabledInternalMDNS) SetAutoAccept(bool)                      {}
func (*disabledInternalMDNS) RequestMdnsEntries()                     {}

func (h *liveServiceHandler) approveExpectedRemote() error {
	if h == nil || h.service == nil || h.hub == nil || !validSKI(h.expectedSKI) {
		return errors.New("expected remote approval configuration invalid")
	}
	h.service.SetAutoAccept(false)
	h.service.UserIsAbleToApproveOrCancelPairingRequests(false)
	h.service.RegisterRemoteSKI(h.expectedSKI)
	h.hub.SetAutoAccept(false)
	h.hub.RegisterRemoteSKI(h.expectedSKI)
	remote := h.hub.ServiceForSKI(h.expectedSKI)
	if remote == nil || !remote.Trusted() || h.service.IsAutoAcceptEnabled() {
		return errors.New("expected remote approval was not installed before start")
	}
	h.mu.Lock()
	h.expectedApproved = true
	h.mu.Unlock()
	return nil
}

func (h *liveServiceHandler) start() error {
	if h == nil || h.server == nil {
		return errors.New("instrumented SHIP server unavailable")
	}
	return h.server.start()
}

func (h *liveServiceHandler) shutdown() {
	if h == nil || h.server == nil {
		return
	}
	h.server.shutdown()
}

func (h *liveServiceHandler) RemoteSKIConnected(_ eebusapi.ServiceInterface, ski string) {
	if !h.allowRemote(ski) {
		go h.hub.CancelPairingWithSKI(ski)
		return
	}
	h.mu.Lock()
	if !h.expectedApproved {
		h.mu.Unlock()
		go h.hub.CancelPairingWithSKI(ski)
		return
	}
	h.generation++
	h.connected = true
	h.connectedAt = time.Now().UTC()
	h.firstSPINE = spineCapture{}
	h.states = append(h.states, connectionStateName(shipapi.ConnectionStateCompleted))
	h.mu.Unlock()
	h.service.SetAutoAccept(false)
	h.service.UserIsAbleToApproveOrCancelPairingRequests(false)
	h.hub.SetAutoAccept(false)
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

func (h *liveServiceHandler) firstSPINESnapshot() spineCapture {
	h.mu.Lock()
	defer h.mu.Unlock()
	capture := h.firstSPINE
	capture.Evidence = spineEvidence{
		EntityTypes:  append([]string(nil), h.firstSPINE.Evidence.EntityTypes...),
		FeatureTypes: append([]string(nil), h.firstSPINE.Evidence.FeatureTypes...),
		UseCaseRefs:  append([]string(nil), h.firstSPINE.Evidence.UseCaseRefs...),
	}
	return capture
}

func (h *liveServiceHandler) captureInboundSPINEPayload(ski string, generation uint64, message []byte, evidence spineEvidence, capturedAt time.Time) {
	if normalizeSKI(ski) != h.expectedSKI || generation == 0 || !validInboundSPINEPayload(message) || evidence.empty() || capturedAt.IsZero() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.connected || h.generation != generation || (h.firstSPINE.Generation == generation && validSHA256Ref(h.firstSPINE.PayloadHash)) {
		return
	}
	h.firstSPINE = spineCapture{
		Evidence:    evidence.normalized(),
		PayloadHash: fullDigestRef(message),
		Generation:  generation,
		CapturedAt:  capturedAt.UTC(),
	}
}

func (h *liveServiceHandler) setWithdrawalTracker(tracker *postWithdrawalTracker) {
	h.mu.Lock()
	h.withdrawal = tracker
	h.mu.Unlock()
}

func (h *liveServiceHandler) recordInboundAttempt(at time.Time) {
	h.mu.Lock()
	tracker := h.withdrawal
	h.mu.Unlock()
	if tracker != nil {
		tracker.recordInboundAttempt(at)
	}
}

func (h *liveServiceHandler) stateSnapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return sortedUnique(h.states)
}

type liveHubReader struct {
	delegate *service.Service
	handler  *liveServiceHandler
}

func (r *liveHubReader) RemoteSKIConnected(ski string) {
	r.delegate.RemoteSKIConnected(ski)
}

func (r *liveHubReader) RemoteSKIDisconnected(ski string) {
	r.delegate.RemoteSKIDisconnected(ski)
}

func (r *liveHubReader) SetupRemoteDevice(ski string, writer shipapi.ShipConnectionDataWriterInterface) shipapi.ShipConnectionDataReaderInterface {
	reader := r.delegate.SetupRemoteDevice(ski, writer)
	generation := r.handler.connectionSnapshot().Generation
	return &payloadCaptureReader{
		delegate: reader,
		capture: func(message []byte) {
			evidence := deriveInboundSPINEProjection(message)
			r.handler.captureInboundSPINEPayload(ski, generation, message, evidence, time.Now().UTC())
		},
	}
}

func (r *liveHubReader) VisibleRemoteServicesUpdated(entries []shipapi.RemoteService) {
	r.delegate.VisibleRemoteServicesUpdated(entries)
}

func (r *liveHubReader) ServiceShipIDUpdate(ski string, shipID string) {
	r.delegate.ServiceShipIDUpdate(ski, shipID)
}

func (r *liveHubReader) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	r.delegate.ServicePairingDetailUpdate(ski, detail)
}

func (r *liveHubReader) AllowWaitingForTrust(ski string) bool {
	return r.delegate.AllowWaitingForTrust(ski)
}

type payloadCaptureReader struct {
	delegate shipapi.ShipConnectionDataReaderInterface
	capture  func([]byte)
}

func (r *payloadCaptureReader) HandleShipPayloadMessage(message []byte) {
	if r == nil || r.delegate == nil {
		return
	}
	r.delegate.HandleShipPayloadMessage(message)
	if r.capture != nil {
		r.capture(append([]byte(nil), message...))
	}
}

func validInboundSPINEPayload(message []byte) bool {
	return !deriveInboundSPINEProjection(message).empty()
}

func deriveInboundSPINEProjection(message []byte) spineEvidence {
	var datagram model.Datagram
	if len(message) == 0 || json.Unmarshal(message, &datagram) != nil {
		return spineEvidence{}
	}
	if datagram.Datagram.Header.MsgCounter == nil && datagram.Datagram.Header.MsgCounterReference == nil {
		return spineEvidence{}
	}
	projection := spineEvidence{}
	for _, command := range datagram.Datagram.Payload.Cmd {
		payload, err := json.Marshal(command)
		if err != nil {
			return spineEvidence{}
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			return spineEvidence{}
		}
		for field, value := range fields {
			if len(value) != 0 && string(value) != "null" {
				projection.FeatureTypes = append(projection.FeatureTypes, "spine-cmd/"+field)
			}
		}
	}
	return projection.normalized()
}

type instrumentedSHIPServer struct {
	port        int
	certificate tls.Certificate
	handler     http.Handler
	discovery   shipapi.MdnsInterface
	hub         *shiphub.Hub
	onAccept    func(time.Time)

	mu         sync.Mutex
	started    bool
	listener   net.Listener
	httpServer *http.Server
}

func (s *instrumentedSHIPServer) start() error {
	if s == nil || s.port < 1 || s.port > 65535 || s.handler == nil || s.discovery == nil || s.hub == nil {
		return errors.New("instrumented SHIP server configuration invalid")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("SHIP listener start failed: %w", err)
	}
	if err := s.discovery.Start(s.hub); err != nil {
		_ = listener.Close()
		return fmt.Errorf("isolated SHIP discovery start failed: %w", err)
	}
	instrumented := &shipAttemptListener{Listener: listener, onAccept: s.onAccept, now: time.Now}
	tlsListener := tls.NewListener(instrumented, &tls.Config{
		Certificates:          []tls.Certificate{s.certificate},
		ClientAuth:            tls.RequireAnyClientCert,
		CipherSuites:          cert.CipherSuites,
		VerifyPeerCertificate: verifySHIPPeerCertificate,
		MinVersion:            tls.VersionTLS12,
	})
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		s.discovery.Shutdown()
		_ = tlsListener.Close()
		return errors.New("instrumented SHIP server already started")
	}
	s.started = true
	s.listener = tlsListener
	s.httpServer = server
	s.mu.Unlock()
	go func() {
		_ = server.Serve(tlsListener)
	}()
	return nil
}

func (s *instrumentedSHIPServer) shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	server := s.httpServer
	listener := s.listener
	s.httpServer = nil
	s.listener = nil
	s.mu.Unlock()

	s.hub.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if server != nil {
		_ = server.Shutdown(ctx)
	}
	if listener != nil {
		_ = listener.Close()
	}
}

type shipAttemptListener struct {
	net.Listener
	onAccept func(time.Time)
	now      func() time.Time
}

func (l *shipAttemptListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err == nil && l.onAccept != nil {
		now := time.Now
		if l.now != nil {
			now = l.now
		}
		l.onAccept(now().UTC())
	}
	return connection, err
}

func verifySHIPPeerCertificate(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	for _, rawCert := range rawCerts {
		certificate, err := x509.ParseCertificate(rawCert)
		if err != nil {
			return err
		}
		if _, err := cert.SkiFromCertificate(certificate); err == nil {
			return nil
		}
	}
	return errors.New("no valid SKI provided in certificate")
}

type negativeWindowClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realNegativeWindowClock struct{}

func (realNegativeWindowClock) Now() time.Time {
	return time.Now().UTC()
}

func (realNegativeWindowClock) After(duration time.Duration) <-chan time.Time {
	return time.After(duration)
}

type postWithdrawalWindowResult struct {
	Satisfied bool
	Err       error
}

func waitPostWithdrawalWindow(ctx context.Context, tracker *postWithdrawalTracker, window time.Duration, clock negativeWindowClock) (bool, error) {
	if tracker == nil || clock == nil || window <= 0 {
		return false, errors.New("post-withdrawal negative window configuration invalid")
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case endedAt := <-clock.After(window):
		satisfied, _, err := tracker.finish(endedAt.UTC(), window)
		return satisfied, err
	}
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
		firstSPINE.PayloadHash,
		firstSPINE.Evidence.dataHash(),
		firstSPINE.CapturedAt.UTC().Format(time.RFC3339Nano),
		b.generationRef(firstSPINE.Generation),
	}
	return keyedDigestRef(b.key, []byte(strings.Join(parts, "\x00")))
}

func (b liveRunBinding) operatorChallenge(transportHash string, firstSPINE spineCapture) operatorChallenge {
	challenge := operatorChallenge{
		Kind:                  "helianthus-eebus-live-proof",
		RunNonce:              b.nonce,
		RunRef:                b.runRef,
		ExpectedRemoteDigest:  b.expectedRemoteDigest,
		InterfaceRef:          b.interfaceRef,
		PortRef:               b.portRef,
		RunStartedAt:          b.startedAt,
		RunExpiresAt:          b.expiresAt,
		TransportHash:         strings.TrimSpace(transportHash),
		FirstPostAccessSPINE:  firstSPINE.Evidence.dataHash(),
		FirstSPINEPayloadHash: strings.TrimSpace(firstSPINE.PayloadHash),
	}
	if firstSPINE.Generation != 0 && validSHA256Ref(firstSPINE.PayloadHash) && !firstSPINE.Evidence.empty() && !firstSPINE.CapturedAt.IsZero() {
		challenge.ConnectionGenerationRef = b.generationRef(firstSPINE.Generation)
		challenge.ChallengeIssuedAt = b.challengeIssuedAt
		challenge.FirstSPINECapturedAt = firstSPINE.CapturedAt.UTC()
	} else {
		challenge.FirstPostAccessSPINE = ""
		challenge.FirstSPINEPayloadHash = ""
	}
	if challenge.TransportHash != "" && challenge.FirstSPINEPayloadHash != "" && challenge.FirstPostAccessSPINE != "" && challenge.ConnectionGenerationRef != "" {
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

func validateG17OperatorProof(proof operatorProofInput, binding liveRunBinding, now time.Time) error {
	proof = proof.normalized()
	if !proof.LANObserverConfirmed || !proof.TrustVisible || !proof.OwnerAccepted {
		return errors.New("G17 operator confirmations incomplete")
	}
	if proof.RunNonce != binding.nonce || proof.RunRef != binding.runRef || proof.ExpectedRemoteDigest != binding.expectedRemoteDigest || proof.InterfaceRef != binding.interfaceRef || proof.PortRef != binding.portRef {
		return errors.New("operator proof run binding mismatch")
	}
	if !proof.RunStartedAt.Equal(binding.startedAt) || !proof.RunExpiresAt.Equal(binding.expiresAt) {
		return errors.New("operator proof run window mismatch")
	}
	if proof.ObservedAt.IsZero() || proof.AcceptedAt.IsZero() || proof.ObservedAt.Before(binding.startedAt) || proof.AcceptedAt.Before(proof.ObservedAt) || proof.AcceptedAt.After(binding.expiresAt) || proof.AcceptedAt.After(now.UTC().Add(30*time.Second)) {
		return errors.New("operator proof timestamps invalid")
	}
	if !validSHA256Ref(proof.EvidenceRef) {
		return errors.New("operator proof evidence hash invalid")
	}
	return nil
}

func validateG19OperatorProof(proof operatorProofInput, binding liveRunBinding, firstSPINE spineCapture, connection connectionSnapshot, now time.Time) error {
	proof = proof.normalized()
	if err := validateG17OperatorProof(proof, binding, now); err != nil {
		return err
	}
	if !proof.InboundTransportObserved {
		return errors.New("G19 inbound transport confirmation incomplete")
	}
	if binding.challengeIssuedAt.IsZero() || firstSPINE.CapturedAt.IsZero() || connection.ConnectedAt.IsZero() || proof.ObservedAt.After(binding.challengeIssuedAt) || proof.AcceptedAt.Before(binding.challengeIssuedAt) || firstSPINE.CapturedAt.Before(connection.ConnectedAt) || firstSPINE.CapturedAt.After(binding.challengeIssuedAt) {
		return errors.New("operator proof timestamps invalid")
	}
	if !proof.ChallengeIssuedAt.Equal(binding.challengeIssuedAt) || !proof.FirstSPINECapturedAt.Equal(firstSPINE.CapturedAt.UTC()) {
		return errors.New("operator proof timestamp binding mismatch")
	}
	if !validSHA256Ref(proof.TransportHash) || !validSHA256Ref(proof.FirstSPINEPayloadHash) {
		return errors.New("operator proof integrity hashes invalid")
	}
	if !connection.Connected || connection.Generation == 0 || firstSPINE.Generation != connection.Generation || !validSHA256Ref(firstSPINE.PayloadHash) || firstSPINE.Evidence.empty() {
		return errors.New("operator proof is not bound to the current connection generation")
	}
	firstSPINEHash := firstSPINE.Evidence.dataHash()
	if proof.ConnectionGenerationRef != binding.generationRef(connection.Generation) || proof.FirstSPINEPayloadHash != firstSPINE.PayloadHash || proof.FirstSPINEHash != firstSPINEHash || proof.ChallengeRef != binding.challenge(proof.TransportHash, firstSPINE) {
		return errors.New("operator proof challenge mismatch")
	}
	return nil
}

func validateOperatorProof(proof operatorProofInput, binding liveRunBinding, firstSPINE spineCapture, connection connectionSnapshot, now time.Time) error {
	return validateG19OperatorProof(proof, binding, firstSPINE, connection, now)
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
	p.FirstSPINEPayloadHash = strings.TrimSpace(p.FirstSPINEPayloadHash)
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

func completedInboundStages(connection connectionSnapshot, proof operatorProofInput, firstSPINE spineCapture, binding liveRunBinding) []transportStage {
	stages := make([]transportStage, 0, len(requiredTransportStages))
	if validateG19OperatorProof(proof, binding, firstSPINE, connection, time.Now()) == nil {
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

func constructCanonicalG19Evidence(opts liveOptions, binding liveRunBinding, proof operatorProofInput, firstSPINE spineCapture, states []string, g19 caseResult, deniedReplay, reconnectReplay negativeObservation, artifact replayArtifact) (*liveGateEvidence, error) {
	stagePayload, err := json.Marshal(sortedUnique(states))
	if err != nil {
		return nil, err
	}
	evidence := buildLiveGateEvidence(opts, binding, proof, firstSPINE, stagePayload, g19, deniedReplay, reconnectReplay, artifact)
	if err := evidence.validateForCase(g19, currentRepoEvidence()); err != nil {
		return nil, err
	}
	return &evidence, nil
}

func failCanonicalG19(result caseResult) caseResult {
	result = result.normalized()
	result.Status = resultFail
	result.Error = "canonical_evidence_construction_failed"
	result.Evidence = append(result.Evidence, "g19-canonical-evidence-failed-closed")
	return result.normalized()
}

func buildLiveGateEvidence(opts liveOptions, binding liveRunBinding, proof operatorProofInput, firstSPINE spineCapture, stagePayload []byte, g19 caseResult, deniedReplay, reconnectReplay negativeObservation, artifact replayArtifact) liveGateEvidence {
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
			DiscoveryIsolation:     "internal-mdns-disabled",
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
			TranscriptHashes:        []string{proof.TransportHash, firstSPINE.PayloadHash, fullDigestRef(stagePayload)},
			FirstSPINEPayloadHash:   firstSPINE.PayloadHash,
			FirstSPINEData:          firstSPINE.Evidence,
			FirstSPINEDataHash:      firstSPINE.Evidence.dataHash(),
		},
		CIReplayAuthority: ciReplayAuthority{
			Result:        resultPass,
			Fixtures:      []replayArtifact{artifact},
			ReplayCommand: "go test ./internal/eebusinteropsmoke -run 'TestG17|TestG19|TestLiveEvidence'",
		},
		NegativeCases: negativeCaseEvidence{
			DeniedAccess: evidenceResult{
				Result:       resultPass,
				Authority:    negativeAuthorityCIReplay,
				LiveObserved: false,
				EvidenceHash: deniedReplay.EvidenceHash,
			},
			ReconnectFailure: evidenceResult{
				Result:       resultPass,
				Authority:    negativeAuthorityCIReplay,
				LiveObserved: false,
				EvidenceHash: reconnectReplay.EvidenceHash,
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
			AcceptedAt: proof.AcceptedAt.UTC(),
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
