package eebusfacade

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	rawReadTokenDomainV1        = "helianthus.eebus.raw.read-token.v1\x00"
	rawRemoteIdentityV1         = "helianthus.eebus.raw.remote-identity.v1\x00"
	rawDispatchIdentityV1       = "helianthus.eebus.raw.dispatch-identity.v1\x00"
	rawRuntimeEpochDomainV1     = "helianthus.eebus.raw.runtime-epoch.v1\x00"
	rawReadTokenLifetimeV1      = time.Minute
	rawMetadataMaximumEntriesV1 = 256
)

var (
	errRawRuntimeEpochMismatch = errors.New("raw runtime epoch changed")
	errRawRemoteNotAdmitted    = errors.New("raw remote is not admitted")
)

type RawFeatureBackend interface {
	FeaturesGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeaturesGetRequestV1,
	) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1)
	FeaturesDataGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeatureDataGetRequestV1,
	) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1)
}

type rawReadTokenIssuer struct {
	key [sha256.Size]byte
}

func (issuer *rawReadTokenIssuer) String() string {
	return "raw_read_token_issuer:[redacted]"
}

func (issuer *rawReadTokenIssuer) GoString() string {
	return issuer.String()
}

func (issuer *rawReadTokenIssuer) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, issuer.String())
}

type rawReadTokenBindingV1 struct {
	Contract        string                    `json:"contract"`
	Runtime         eebusraw.RuntimeBindingV1 `json:"runtime"`
	Target          eebusraw.FeatureTargetV1  `json:"target"`
	RequestHash     eebusraw.HashV1           `json:"request_hash"`
	BeforeImageHash eebusraw.HashV1           `json:"before_image_hash"`
	PrincipalClass  string                    `json:"principal_class"`
	Scope           eebusraw.AuthScopeV1      `json:"scope"`
	Tool            eebusraw.ToolV1           `json:"tool"`
	MaskTier        eebusraw.MaskTier         `json:"mask_tier"`
	ExpiresAt       time.Time                 `json:"expires_at"`
	Reusable        bool                      `json:"reusable"`
	ReusePolicy     string                    `json:"reuse_policy"`
}

func newRawReadTokenIssuer(key []byte) (*rawReadTokenIssuer, error) {
	if len(key) < sha256.Size {
		return nil, errors.New("protected raw READ token material is unavailable")
	}
	issuer := &rawReadTokenIssuer{}
	if len(key) == sha256.Size {
		copy(issuer.key[:], key)
	} else {
		issuer.key = sha256.Sum256(key)
	}
	return issuer, nil
}

func newRuntimeRawReadTokenIssuer(nodeToken string) (*rawReadTokenIssuer, error) {
	nodeToken = strings.TrimSpace(nodeToken)
	if nodeToken == "" {
		return nil, errors.New("protected raw READ token material is unavailable")
	}
	entropy := make([]byte, sha256.Size)
	if _, err := rand.Read(entropy); err != nil {
		return nil, errors.New("protected raw READ token material is unavailable")
	}
	defer clear(entropy)
	mac := hmac.New(sha256.New, entropy)
	_, _ = mac.Write([]byte(rawReadTokenDomainV1))
	_, _ = mac.Write([]byte(nodeToken))
	key := mac.Sum(nil)
	defer clear(key)
	return newRawReadTokenIssuer(key)
}

func rawRuntimeEpochForIdentity(localSKI string) (uint64, error) {
	localSKI = strings.ToLower(strings.TrimSpace(localSKI))
	if !validRuntimeSKI(localSKI) {
		return 0, errors.New("raw runtime identity is unavailable")
	}
	digest := sha256.Sum256([]byte(rawRuntimeEpochDomainV1 + localSKI))
	epoch := binary.BigEndian.Uint64(digest[:8]) & ((uint64(1) << 53) - 1)
	if epoch == 0 {
		epoch = 1
	}
	return epoch, nil
}

func rawRuntimeEpochProvider(
	firstTrust *runtimeFirstTrustResources,
	fallback uint64,
) func() uint64 {
	return func() uint64 {
		if firstTrust == nil || firstTrust.coordinator == nil {
			return fallback
		}
		coordinator := firstTrust.coordinator
		coordinator.mu.Lock()
		repairSequence := coordinator.controlView.control.repairSequence
		coordinator.mu.Unlock()
		if repairSequence == ^uint64(0) {
			return 0
		}
		return repairSequence + 1
	}
}

func (issuer *rawReadTokenIssuer) issue(
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
	runtime eebusraw.RuntimeBindingV1,
	requestHash eebusraw.HashV1,
	beforeImageHash eebusraw.HashV1,
	receivedAt time.Time,
) (eebusraw.ReadTokenV1, error) {
	if issuer == nil || runtime.RuntimeEpoch == 0 || runtime.ConnectionGeneration == 0 ||
		requestHash == "" || beforeImageHash == "" || receivedAt.IsZero() {
		return eebusraw.ReadTokenV1{}, errors.New("raw READ token binding is incomplete")
	}
	binding := rawReadTokenBindingV1{
		Contract:        "helianthus.eebus.raw.read-token-binding.v1",
		Runtime:         runtime,
		Target:          target.Clone(),
		RequestHash:     requestHash,
		BeforeImageHash: beforeImageHash,
		PrincipalClass:  auth.PrincipalClass,
		Scope:           auth.Scope,
		Tool:            auth.Tool,
		MaskTier:        auth.MaskTier,
		ExpiresAt:       receivedAt.UTC().Add(rawReadTokenLifetimeV1),
		Reusable:        false,
		ReusePolicy:     "single_use",
	}
	bindingHash, err := eebusraw.CanonicalSHA256V1(binding)
	if err != nil {
		return eebusraw.ReadTokenV1{}, errors.New("raw READ token binding cannot be committed")
	}
	mac := hmac.New(sha256.New, issuer.key[:])
	_, _ = mac.Write([]byte(rawReadTokenDomainV1))
	_, _ = mac.Write([]byte(bindingHash))
	return eebusraw.ReadTokenV1{
		ReadToken:   base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
		Reusable:    binding.Reusable,
		ExpiresAt:   binding.ExpiresAt,
		BindingHash: bindingHash,
	}, nil
}

type rawRemoteLease struct {
	ski          string
	shipID       string
	address      spinemodel.AddressDeviceType
	identity     executor.ExactRemoteIdentity
	generation   executor.ExactConnectionGeneration
	runtimeEpoch uint64
	remote       spineapi.DeviceRemoteInterface
	sender       spineapi.CorrelatedRoundTripper
	retired      bool
	inFlight     map[uint64]context.CancelFunc
}

type rawResponseMetadataKey struct {
	address     spinemodel.AddressDeviceType
	generation  executor.ExactConnectionGeneration
	correlation spinemodel.MsgCounterType
}

type rawProtocolMetadata struct {
	request  []eebusraw.OpaqueObservationV1
	response []eebusraw.OpaqueObservationV1
}

type rawFeatureRuntimeBridge struct {
	mu sync.Mutex

	local               spineapi.DeviceLocalInterface
	runtimeEpoch        func() uint64
	now                 func() time.Time
	tokenIssuer         *rawReadTokenIssuer
	leasesBySKI         map[string]*rawRemoteLease
	leasesByAddr        map[spinemodel.AddressDeviceType]*rawRemoteLease
	generationHighWater map[string]uint64
	pendingGeneration   map[string]uint64
	generationStore     rawConnectionGenerationStore
	inventory           map[string]eebusraw.FeaturesGetDataV1
	nextDispatch        uint64
	responseMetadata    map[rawResponseMetadataKey]rawProtocolMetadata
}

var _ executor.ExactRemoteRuntime = (*rawFeatureRuntimeBridge)(nil)

func newRawFeatureRuntimeBridge(
	local spineapi.DeviceLocalInterface,
	runtimeEpoch func() uint64,
	now func() time.Time,
	issuer *rawReadTokenIssuer,
) *rawFeatureRuntimeBridge {
	return newRawFeatureRuntimeBridgeWithGenerationStore(local, runtimeEpoch, now, issuer, nil)
}

func newRawFeatureRuntimeBridgeWithGenerationStore(
	local spineapi.DeviceLocalInterface,
	runtimeEpoch func() uint64,
	now func() time.Time,
	issuer *rawReadTokenIssuer,
	generationStore rawConnectionGenerationStore,
) *rawFeatureRuntimeBridge {
	return &rawFeatureRuntimeBridge{
		local:               local,
		runtimeEpoch:        runtimeEpoch,
		now:                 now,
		tokenIssuer:         issuer,
		leasesBySKI:         make(map[string]*rawRemoteLease),
		leasesByAddr:        make(map[spinemodel.AddressDeviceType]*rawRemoteLease),
		generationHighWater: make(map[string]uint64),
		pendingGeneration:   make(map[string]uint64),
		generationStore:     generationStore,
		inventory:           make(map[string]eebusraw.FeaturesGetDataV1),
		responseMetadata:    make(map[rawResponseMetadataKey]rawProtocolMetadata),
	}
}

func (bridge *rawFeatureRuntimeBridge) admitRemote(
	ski string,
	shipID string,
	generation uint64,
	remote spineapi.DeviceRemoteInterface,
) error {
	lease, err := bridge.rawRemoteLease(ski, shipID, generation, remote)
	if err != nil {
		return err
	}

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	pending := bridge.pendingGeneration[lease.ski]
	if generation <= bridge.generationHighWater[lease.ski] || (pending != 0 && pending != generation) {
		return errors.New("exact remote connection generation is not new")
	}
	if bridge.generationStore != nil && bridge.generationStore.advance(lease.runtimeEpoch, lease.ski, generation) != nil {
		return errors.New("exact remote connection generation cannot be persisted")
	}
	if prior := bridge.leasesBySKI[lease.ski]; prior != nil {
		bridge.retireLeaseLocked(prior, false)
	}
	if prior := bridge.leasesByAddr[lease.address]; prior != nil {
		bridge.retireLeaseLocked(prior, false)
	}
	bridge.generationHighWater[lease.ski] = generation
	delete(bridge.pendingGeneration, lease.ski)
	bridge.leasesBySKI[lease.ski] = lease
	bridge.leasesByAddr[lease.address] = lease
	bridge.captureRemoteInventoryLocked(lease)
	return nil
}

func (bridge *rawFeatureRuntimeBridge) beginConnectionGeneration(ski string, generation uint64) error {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !validRuntimeSKI(ski) || generation == 0 {
		return errors.New("exact remote connection generation is invalid")
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if generation <= bridge.generationHighWater[ski] || generation <= bridge.pendingGeneration[ski] {
		return errors.New("exact remote connection generation is not new")
	}
	if prior := bridge.leasesBySKI[ski]; prior != nil {
		bridge.retireLeaseLocked(prior, true)
	}
	bridge.pendingGeneration[ski] = generation
	return nil
}

func (bridge *rawFeatureRuntimeBridge) refreshRemote(
	ski string,
	shipID string,
	generation uint64,
	remote spineapi.DeviceRemoteInterface,
) error {
	refreshed, err := bridge.rawRemoteLease(ski, shipID, generation, remote)
	if err != nil {
		return err
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesBySKI[refreshed.ski]
	if lease == nil {
		return errRawRemoteNotAdmitted
	}
	if lease.retired || lease.generation != refreshed.generation ||
		generation != bridge.generationHighWater[refreshed.ski] ||
		lease.shipID != refreshed.shipID || lease.address != refreshed.address ||
		lease.runtimeEpoch != refreshed.runtimeEpoch ||
		!sameRawRuntimeValue(lease.sender, refreshed.sender) {
		return errors.New("stale raw remote refresh rejected")
	}
	lease.remote = refreshed.remote
	bridge.captureRemoteInventoryLocked(lease)
	return nil
}

func (bridge *rawFeatureRuntimeBridge) rawRemoteLease(
	ski string,
	shipID string,
	generation uint64,
	remote spineapi.DeviceRemoteInterface,
) (*rawRemoteLease, error) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	shipID = strings.TrimSpace(shipID)
	if !validRuntimeSKI(ski) || shipID == "" || generation == 0 ||
		isNilRawRuntimeValue(remote) || remote.Address() == nil ||
		*remote.Address() == "" || !strings.EqualFold(strings.TrimSpace(remote.Ski()), ski) {
		return nil, errors.New("exact remote admission binding is incomplete")
	}
	sender, ok := remote.Sender().(spineapi.CorrelatedRoundTripper)
	if !ok || isNilRawRuntimeValue(sender) {
		return nil, errors.New("exact remote correlated round-tripper is unavailable")
	}
	address := *remote.Address()
	runtimeEpoch := bridge.currentRuntimeEpoch()
	if runtimeEpoch == 0 {
		return nil, errors.New("exact remote runtime epoch is unavailable")
	}
	return &rawRemoteLease{
		ski:          ski,
		shipID:       shipID,
		address:      address,
		identity:     exactRawRemoteIdentity(ski, shipID, runtimeEpoch),
		generation:   executor.ExactConnectionGeneration(generation),
		runtimeEpoch: runtimeEpoch,
		remote:       remote,
		sender:       sender,
		inFlight:     make(map[uint64]context.CancelFunc),
	}, nil
}

func (bridge *rawFeatureRuntimeBridge) retireRemote(ski string, generation uint64) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	bridge.mu.Lock()
	lease := bridge.leasesBySKI[ski]
	if lease == nil || uint64(lease.generation) != generation {
		bridge.mu.Unlock()
		return
	}
	bridge.retireLeaseLocked(lease, true)
	bridge.mu.Unlock()
}

func (bridge *rawFeatureRuntimeBridge) retireAll() {
	bridge.mu.Lock()
	leases := make([]*rawRemoteLease, 0, len(bridge.leasesBySKI))
	for _, lease := range bridge.leasesBySKI {
		leases = append(leases, lease)
	}
	for _, lease := range leases {
		bridge.retireLeaseLocked(lease, true)
	}
	bridge.mu.Unlock()
}

func (bridge *rawFeatureRuntimeBridge) retireLeaseLocked(
	lease *rawRemoteLease,
	cacheInventory bool,
) {
	if lease == nil || lease.retired {
		return
	}
	lease.retired = true
	if bridge.leasesBySKI[lease.ski] == lease {
		delete(bridge.leasesBySKI, lease.ski)
	}
	if bridge.leasesByAddr[lease.address] == lease {
		delete(bridge.leasesByAddr, lease.address)
	}
	for _, cancel := range lease.inFlight {
		cancel()
	}
	if !cacheInventory {
		bridge.removeRemoteInventoryLocked(lease.ski)
		return
	}
	for key, item := range bridge.inventory {
		if item.Feature.RemoteSKI == lease.ski &&
			item.Runtime.ConnectionGeneration == uint64(lease.generation) {
			item.Source = eebusraw.ObservationSourceV1Cache
			bridge.inventory[key] = item
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) ResolveExactRemoteDevice(
	address spinemodel.AddressDeviceType,
) (spineapi.DeviceRemoteInterface, error) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesByAddr[address]
	if lease == nil || lease.retired {
		return nil, executor.ErrExactTargetNotFound
	}
	return lease.remote, nil
}

func (bridge *rawFeatureRuntimeBridge) RoundTripIfCurrent(
	ctx context.Context,
	expected executor.ExactRemoteBinding,
	request spineapi.CorrelatedRequest,
) (spineapi.CorrelatedResponse, error) {
	failure := func(kind executor.ExactRemoteBindingFailure) (spineapi.CorrelatedResponse, error) {
		return spineapi.CorrelatedResponse{}, &executor.ExactRemoteBindingError{Failure: kind}
	}
	if expected.DeviceAddress == "" || expected.RemoteIdentity == "" ||
		expected.ConnectionGeneration == 0 {
		return failure(executor.ExactRemoteBindingProofMissing)
	}
	if request.Destination.Device == nil ||
		*request.Destination.Device != expected.DeviceAddress {
		return failure(executor.ExactRemoteBindingAddressMismatch)
	}

	if ctx == nil {
		return spineapi.CorrelatedResponse{}, context.Canceled
	}
	bridge.mu.Lock()
	lease := bridge.leasesByAddr[expected.DeviceAddress]
	if lease == nil || lease.retired {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingProofMissing)
	}
	if lease.address != expected.DeviceAddress {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingAddressMismatch)
	}
	currentAddress := lease.remote.Address()
	if currentAddress == nil || *currentAddress != lease.address {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingAddressMismatch)
	}
	if !strings.EqualFold(strings.TrimSpace(lease.remote.Ski()), lease.ski) {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingIdentityMismatch)
	}
	if !sameRawRuntimeValue(lease.remote.Sender(), lease.sender) ||
		lease.sender.Stats().Closed {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingGenerationMismatch)
	}
	if bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
		bridge.mu.Unlock()
		return spineapi.CorrelatedResponse{}, errors.Join(
			errRawRuntimeEpochMismatch,
			&executor.ExactRemoteBindingError{Failure: executor.ExactRemoteBindingIdentityMismatch},
		)
	}
	if err := ctx.Err(); err != nil {
		bridge.mu.Unlock()
		return spineapi.CorrelatedResponse{}, err
	}
	feature, function, err := exactRawDispatchFeature(lease, request)
	if err != nil {
		bridge.mu.Unlock()
		return spineapi.CorrelatedResponse{}, err
	}
	if exactRawDispatchIdentity(lease, feature, function) != expected.RemoteIdentity {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingIdentityMismatch)
	}
	if lease.generation != expected.ConnectionGeneration {
		bridge.mu.Unlock()
		return failure(executor.ExactRemoteBindingGenerationMismatch)
	}

	dispatchContext, cancel := context.WithCancel(ctx)
	bridge.nextDispatch++
	dispatchID := bridge.nextDispatch
	lease.inFlight[dispatchID] = cancel
	sender := lease.sender
	bridge.mu.Unlock()

	response, roundTripErr := sender.RoundTrip(dispatchContext, request)
	cancel()

	bridge.mu.Lock()
	delete(lease.inFlight, dispatchID)
	current := !lease.retired &&
		bridge.leasesBySKI[lease.ski] == lease &&
		bridge.leasesByAddr[lease.address] == lease &&
		bridge.currentRuntimeEpoch() == lease.runtimeEpoch
	if current && roundTripErr == nil {
		bridge.storeResponseMetadataLocked(lease, request, response)
	}
	bridge.mu.Unlock()
	if !current {
		return failure(executor.ExactRemoteBindingGenerationMismatch)
	}
	return response, roundTripErr
}

func (bridge *rawFeatureRuntimeBridge) featuresGet(
	_ context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeaturesGetRequestV1,
) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesGet); terminal != nil {
		return eebusraw.FeaturesGetDataV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeaturesGetRequestV1(request); terminal != nil {
		return eebusraw.FeaturesGetDataV1{}, terminal
	}

	bridge.mu.Lock()
	if lease := bridge.leasesBySKI[strings.ToLower(request.Target.RemoteSKI)]; lease != nil {
		if bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
			bridge.mu.Unlock()
			return eebusraw.FeaturesGetDataV1{}, eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1RuntimeEpochMismatch,
				"runtime epoch changed before raw feature lookup",
				true,
				eebusraw.SourceLayerV1Runtime,
			)
		}
		feature, err := exactRawRemoteFeature(lease, request.Target)
		if err == nil {
			data, buildErr := bridge.inventoryForFeatureLocked(lease, feature)
			if buildErr != nil {
				bridge.mu.Unlock()
				return eebusraw.FeaturesGetDataV1{}, rawInternalError()
			}
			bridge.inventory[rawFeatureLocatorKey(request.Target)] = data.Clone()
			bridge.mu.Unlock()
			return data.Clone(), nil
		}
	}
	if data, found := bridge.inventory[rawFeatureLocatorKey(request.Target)]; found {
		if data.Source == eebusraw.ObservationSourceV1Live {
			data.Source = eebusraw.ObservationSourceV1Cache
			bridge.inventory[rawFeatureLocatorKey(request.Target)] = data.Clone()
		}
		bridge.mu.Unlock()
		return data.Clone(), nil
	}
	bridge.mu.Unlock()
	return eebusraw.FeaturesGetDataV1{}, eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1NotFound,
		"exact raw feature is unavailable",
		true,
		eebusraw.SourceLayerV1Runtime,
	)
}

func (bridge *rawFeatureRuntimeBridge) featuresDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeatureDataGetRequestV1,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataGet); terminal != nil {
		return eebusraw.FeatureDataGetDataV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(request); terminal != nil {
		return eebusraw.FeatureDataGetDataV1{}, terminal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := request.TimeoutMS
	if timeout == 0 {
		timeout = eebusraw.MaximumReadTimeoutMSV1
	}
	readContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	data := eebusraw.FeatureDataGetDataV1{
		Results:  make([]eebusraw.ReadObservationV1, 0, len(request.Targets)),
		Failures: make([]eebusraw.ReadFailureV1, 0),
	}
	for index, target := range request.Targets {
		if err := readContext.Err(); err != nil {
			terminal := translateRawExecutorError(err)
			data.Failures = append(data.Failures, eebusraw.ReadFailureV1{
				TargetIndex: uint64(index),
				Target:      target.Clone(),
				Error:       terminal.Clone(),
			})
			continue
		}
		observation, terminal := bridge.readTarget(readContext, auth, target)
		if terminal != nil {
			data.Failures = append(data.Failures, eebusraw.ReadFailureV1{
				TargetIndex: uint64(index),
				Target:      target.Clone(),
				Error:       terminal.Clone(),
			})
			continue
		}
		data.Results = append(data.Results, observation)
	}
	data.Complete = len(data.Failures) == 0
	if data.Complete {
		return data.Clone(), nil
	}
	if len(data.Results) == 0 {
		terminal := data.Failures[0].Error.Clone()
		return eebusraw.FeatureDataGetDataV1{}, &terminal
	}
	return data.Clone(), eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1PartialResult,
		"one or more raw READ targets failed",
		rawFailuresRetriable(data.Failures),
		eebusraw.SourceLayerV1Runtime,
	)
}

func (bridge *rawFeatureRuntimeBridge) readTarget(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	exactRequest, runtime, terminal := bridge.exactReadRequest(target)
	if terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	result, err := executor.NewExactFeatureExecutor(bridge.local, bridge).Execute(ctx, exactRequest)
	exactUnknown, unknownErr := rawExactUnknownObservations(result.UnknownFields)
	protocolMetadata := bridge.takeResponseMetadata(
		*exactRequest.Target.Address.Device,
		exactRequest.Target.ConnectionGeneration,
		result.CorrelationKey,
	)
	if unknownErr != nil {
		return eebusraw.ReadObservationV1{}, translateRawExecutorError(unknownErr)
	}
	if err != nil {
		return eebusraw.ReadObservationV1{}, translateRawExecutorError(err)
	}
	requestValue, err := rawCommandValue(result.Request, false)
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawDecodeError()
	}
	responseValue, err := rawCommandValue(result.Response, true)
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawDecodeError()
	}
	requestMessage := eebusraw.ProtocolMessageV1{
		Classifier:     strings.ToUpper(string(spinemodel.CmdClassifierTypeRead)),
		CorrelationKey: uint64(result.CorrelationKey),
		Function:       target.Function,
		Data:           &requestValue,
		Unknown:        cloneRawOpaqueObservations(protocolMetadata.request),
	}
	responseMessage := eebusraw.ProtocolMessageV1{
		Classifier:     strings.ToUpper(string(spinemodel.CmdClassifierTypeReply)),
		CorrelationKey: uint64(result.CorrelationKey),
		Function:       target.Function,
		Data:           &responseValue,
		Unknown:        mergeRawOpaqueObservations(protocolMetadata.response, exactUnknown),
	}
	requestHash, err := eebusraw.CanonicalSHA256V1(requestMessage)
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawDecodeError()
	}
	beforeImageHash, err := responseValue.ComputeHash()
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawDecodeError()
	}
	receivedAt := result.RespondedAt.UTC()
	token, err := bridge.tokenIssuer.issue(
		auth,
		target,
		runtime,
		requestHash,
		beforeImageHash,
		receivedAt,
	)
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawInternalError()
	}
	observation := eebusraw.ReadObservationV1{
		Target:      target.Clone(),
		Runtime:     runtime,
		RawRequest:  requestMessage,
		RawResponse: responseMessage,
		Value:       responseValue,
		Unknown: mergeRawOpaqueObservations(
			protocolMetadata.request,
			protocolMetadata.response,
			exactUnknown,
		),
		RequestedAt:   result.RequestedAt.UTC(),
		ReceivedAt:    receivedAt,
		DataTimestamp: receivedAt,
		Source:        eebusraw.ObservationSourceV1Live,
		ReadToken:     token,
	}
	observation.DataHash, err = observation.ComputeDataHash()
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawInternalError()
	}
	return observation.Clone(), nil
}

func (bridge *rawFeatureRuntimeBridge) exactReadRequest(
	target eebusraw.FeatureTargetV1,
) (executor.ExactFeatureRequest, eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesBySKI[strings.ToLower(target.RemoteSKI)]
	if lease == nil {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"exact remote session is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	if bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RuntimeEpochMismatch,
			"runtime epoch changed before raw READ dispatch",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	feature, err := exactRawRemoteFeature(lease, target.Locator())
	if err != nil {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1NotFound,
			"exact raw feature target does not match current topology",
			false,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	operations := feature.Operations()[spinemodel.FunctionType(target.Function)]
	if operations == nil || !operations.Read() {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1UnsupportedOperation,
			"exact raw feature does not support full READ",
			false,
			eebusraw.SourceLayerV1Executor,
		)
	}
	source, found := exactRawSourceAddress(bridge.local, feature)
	if !found {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"compatible local READ source is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	epoch := lease.runtimeEpoch
	if epoch == 0 {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RuntimeEpochMismatch,
			"runtime epoch is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	runtime := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         epoch,
		ConnectionGeneration: uint64(lease.generation),
	}
	return executor.ExactFeatureRequest{
		Source: source,
		Target: executor.ExactFeatureTarget{
			Address:     cloneRawFeatureAddress(*feature.Address()),
			FeatureType: feature.Type(),
			Role:        feature.Role(),
			Function:    spinemodel.FunctionType(target.Function),
			RemoteIdentity: exactRawDispatchIdentity(
				lease,
				feature,
				spinemodel.FunctionType(target.Function),
			),
			ConnectionGeneration: lease.generation,
		},
		Operation: executor.ExactFeatureOperationRead,
	}, runtime, nil
}

func (bridge *rawFeatureRuntimeBridge) currentRuntimeEpoch() uint64 {
	if bridge == nil || bridge.runtimeEpoch == nil {
		return 0
	}
	return bridge.runtimeEpoch()
}

func (bridge *rawFeatureRuntimeBridge) captureRemoteInventoryLocked(lease *rawRemoteLease) {
	bridge.removeRemoteInventoryLocked(lease.ski)
	for _, entity := range lease.remote.Entities() {
		if isNilRawRuntimeValue(entity) {
			continue
		}
		for _, feature := range entity.Features() {
			if isNilRawRuntimeValue(feature) || feature.Address() == nil {
				continue
			}
			current := lease.remote.FeatureByAddress(feature.Address())
			if isNilRawRuntimeValue(current) {
				continue
			}
			data, err := bridge.inventoryForFeatureLocked(lease, current)
			if err == nil {
				bridge.inventory[rawFeatureLocatorKey(data.Feature)] = data
			}
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) removeRemoteInventoryLocked(ski string) {
	for key, item := range bridge.inventory {
		if item.Feature.RemoteSKI == ski {
			delete(bridge.inventory, key)
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) inventoryForFeatureLocked(
	lease *rawRemoteLease,
	feature spineapi.FeatureRemoteInterface,
) (eebusraw.FeaturesGetDataV1, error) {
	if feature.Address() == nil || feature.Address().Device == nil ||
		feature.Address().Feature == nil || len(feature.Address().Entity) == 0 {
		return eebusraw.FeaturesGetDataV1{}, errors.New("raw feature address is incomplete")
	}
	if len(feature.Operations()) > 512 {
		return eebusraw.FeaturesGetDataV1{}, errors.New("raw feature function inventory exceeds the size limit")
	}
	functionNames := make([]string, 0, len(feature.Operations()))
	for function := range feature.Operations() {
		if function == "" || utf8.RuneCountInString(string(function)) > 256 {
			return eebusraw.FeaturesGetDataV1{}, errors.New("raw feature function identity is invalid")
		}
		functionNames = append(functionNames, string(function))
	}
	sort.Strings(functionNames)
	functions := make([]eebusraw.FunctionDescriptorV1, 0, len(functionNames))
	for _, name := range functionNames {
		operations := feature.Operations()[spinemodel.FunctionType(name)]
		if operations == nil {
			continue
		}
		functions = append(functions, eebusraw.FunctionDescriptorV1{
			Function: name,
			PossibleOperations: eebusraw.FullOperationsV1{
				Read:  operations.Read(),
				Write: operations.Write(),
			},
			Changeable: eebusraw.ChangeabilityV1Unknown,
			Constraints: eebusraw.ConstraintSetV1{
				Status: eebusraw.ConstraintStatusV1Unknown,
			},
		})
	}
	entity := make([]uint64, len(feature.Address().Entity))
	for index, part := range feature.Address().Entity {
		entity[index] = uint64(part)
	}
	data := eebusraw.FeaturesGetDataV1{
		Feature: eebusraw.FeatureLocatorV1{
			RemoteSKI:      lease.ski,
			SHIPID:         lease.shipID,
			DeviceAddress:  string(*feature.Address().Device),
			EntityAddress:  entity,
			FeatureAddress: uint64(*feature.Address().Feature),
			FeatureType:    strings.ToLower(string(feature.Type())),
			FeatureRole:    eebusraw.FeatureRoleV1(feature.Role()),
		},
		Functions: functions,
		Runtime: eebusraw.RuntimeBindingV1{
			RuntimeEpoch:         lease.runtimeEpoch,
			ConnectionGeneration: uint64(lease.generation),
		},
		DataTimestamp: bridge.timestamp(),
		Source:        eebusraw.ObservationSourceV1Live,
	}
	if description := feature.Description(); description != nil {
		if utf8.RuneCountInString(string(*description)) > 4096 {
			return eebusraw.FeaturesGetDataV1{}, errors.New("raw feature description exceeds the size limit")
		}
		data.Description = string(*description)
	}
	if data.Runtime.RuntimeEpoch == 0 {
		return eebusraw.FeaturesGetDataV1{}, errors.New("runtime epoch is unavailable")
	}
	hash, err := data.ComputeDataHash()
	if err != nil {
		return eebusraw.FeaturesGetDataV1{}, err
	}
	data.DataHash = hash
	return data, nil
}

func (bridge *rawFeatureRuntimeBridge) timestamp() time.Time {
	if bridge == nil || bridge.now == nil {
		return time.Unix(0, 0).UTC()
	}
	value := bridge.now().UTC()
	if value.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return value
}

func exactRawRemoteFeature(
	lease *rawRemoteLease,
	locator eebusraw.FeatureLocatorV1,
) (spineapi.FeatureRemoteInterface, error) {
	if lease == nil || lease.ski != strings.ToLower(locator.RemoteSKI) ||
		lease.shipID != locator.SHIPID || string(lease.address) != locator.DeviceAddress {
		return nil, errors.New("exact remote identity does not match")
	}
	address := rawModelFeatureAddress(locator.DeviceAddress, locator.EntityAddress, locator.FeatureAddress)
	feature := lease.remote.FeatureByAddress(&address)
	if isNilRawRuntimeValue(feature) ||
		!strings.EqualFold(string(feature.Type()), locator.FeatureType) ||
		feature.Role() != spinemodel.RoleType(locator.FeatureRole) {
		return nil, errors.New("exact remote feature does not match")
	}
	return feature, nil
}

func exactRawDispatchFeature(
	lease *rawRemoteLease,
	request spineapi.CorrelatedRequest,
) (spineapi.FeatureRemoteInterface, spinemodel.FunctionType, error) {
	if lease == nil || lease.retired ||
		request.Classifier != spinemodel.CmdClassifierTypeRead ||
		request.Destination.Device == nil ||
		*request.Destination.Device != lease.address ||
		len(request.Cmd.Filter) != 0 || request.Cmd.Function != nil {
		return nil, "", executor.ErrExactTargetMismatch
	}
	data, err := request.Cmd.Data()
	if err != nil || data == nil || data.Function == nil || *data.Function == "" {
		return nil, "", executor.ErrExactTargetMismatch
	}
	feature := lease.remote.FeatureByAddress(&request.Destination)
	if isNilRawRuntimeValue(feature) || feature.Address() == nil ||
		!equalRawFeatureAddress(*feature.Address(), request.Destination) {
		return nil, "", executor.ErrExactTargetMismatch
	}
	operations := feature.Operations()[*data.Function]
	if operations == nil || !operations.Read() {
		return nil, "", executor.ErrExactTargetMismatch
	}
	return feature, *data.Function, nil
}

func exactRawDispatchIdentity(
	lease *rawRemoteLease,
	feature spineapi.FeatureRemoteInterface,
	function spinemodel.FunctionType,
) executor.ExactRemoteIdentity {
	if lease == nil || isNilRawRuntimeValue(feature) || feature.Address() == nil {
		return ""
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(rawDispatchIdentityV1))
	_, _ = digest.Write([]byte(lease.identity))
	for _, value := range []string{
		feature.Address().String(),
		string(feature.Type()),
		string(feature.Role()),
		string(function),
	} {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(value))
	}
	return executor.ExactRemoteIdentity("sha256:" + hex.EncodeToString(digest.Sum(nil)))
}

func equalRawFeatureAddress(
	left spinemodel.FeatureAddressType,
	right spinemodel.FeatureAddressType,
) bool {
	if left.Device == nil || right.Device == nil ||
		left.Feature == nil || right.Feature == nil ||
		*left.Device != *right.Device || *left.Feature != *right.Feature ||
		len(left.Entity) != len(right.Entity) {
		return false
	}
	for index := range left.Entity {
		if left.Entity[index] != right.Entity[index] {
			return false
		}
	}
	return true
}

func exactRawSourceAddress(
	local spineapi.DeviceLocalInterface,
	target spineapi.FeatureRemoteInterface,
) (spinemodel.FeatureAddressType, bool) {
	if isNilRawRuntimeValue(local) || local.Address() == nil {
		return spinemodel.FeatureAddressType{}, false
	}
	var candidates []spinemodel.FeatureAddressType
	for _, entity := range local.Entities() {
		if isNilRawRuntimeValue(entity) {
			continue
		}
		for _, feature := range entity.Features() {
			if isNilRawRuntimeValue(feature) || feature.Address() == nil {
				continue
			}
			compatibleType := feature.Type() == target.Type() ||
				feature.Type() == spinemodel.FeatureTypeTypeGeneric
			compatibleRole := target.Role() == spinemodel.RoleTypeServer &&
				feature.Role() == spinemodel.RoleTypeClient ||
				target.Role() == spinemodel.RoleTypeClient &&
					feature.Role() == spinemodel.RoleTypeServer ||
				target.Role() == spinemodel.RoleTypeSpecial &&
					feature.Role() == spinemodel.RoleTypeSpecial
			if compatibleType && compatibleRole {
				candidates = append(candidates, cloneRawFeatureAddress(*feature.Address()))
			}
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].String() < candidates[right].String()
	})
	if len(candidates) == 0 {
		return spinemodel.FeatureAddressType{}, false
	}
	return candidates[0], true
}

func (bridge *rawFeatureRuntimeBridge) storeResponseMetadataLocked(
	lease *rawRemoteLease,
	request spineapi.CorrelatedRequest,
	response spineapi.CorrelatedResponse,
) {
	metadata := rawProtocolMetadata{
		request:  rawRequestObservations(request),
		response: rawResponseHeaderObservations(response.Header),
	}
	if len(metadata.request) == 0 && len(metadata.response) == 0 {
		return
	}
	key := rawResponseMetadataKey{
		address:     lease.address,
		generation:  lease.generation,
		correlation: response.CorrelationKey,
	}
	if _, exists := bridge.responseMetadata[key]; !exists &&
		len(bridge.responseMetadata) >= rawMetadataMaximumEntriesV1 {
		return
	}
	bridge.responseMetadata[key] = cloneRawProtocolMetadata(metadata)
}

func (bridge *rawFeatureRuntimeBridge) takeResponseMetadata(
	address spinemodel.AddressDeviceType,
	generation executor.ExactConnectionGeneration,
	correlation spinemodel.MsgCounterType,
) rawProtocolMetadata {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	key := rawResponseMetadataKey{
		address:     address,
		generation:  generation,
		correlation: correlation,
	}
	result := cloneRawProtocolMetadata(bridge.responseMetadata[key])
	delete(bridge.responseMetadata, key)
	return result
}

func rawRequestObservations(
	request spineapi.CorrelatedRequest,
) []eebusraw.OpaqueObservationV1 {
	const source = "spine-go/api.CorrelatedRequest"
	return rawOpaqueObservations([]struct {
		path  string
		value any
	}{
		{path: "/request/ackRequest", value: request.AckRequest},
		{path: "/request/addressDestination", value: request.Destination},
		{path: "/request/addressSource", value: request.Source},
	}, source)
}

func rawResponseHeaderObservations(
	header spinemodel.HeaderType,
) []eebusraw.OpaqueObservationV1 {
	const source = "spine-go/api.CorrelatedResponse.Header"
	return rawOpaqueObservations([]struct {
		path  string
		value any
	}{
		{path: "/header/ackRequest", value: header.AckRequest},
		{path: "/header/addressDestination", value: header.AddressDestination},
		{path: "/header/addressOriginator", value: header.AddressOriginator},
		{path: "/header/addressSource", value: header.AddressSource},
		{path: "/header/msgCounter", value: header.MsgCounter},
		{path: "/header/msgCounterReference", value: header.MsgCounterReference},
		{path: "/header/specificationVersion", value: header.SpecificationVersion},
		{path: "/header/timestamp", value: header.Timestamp},
	}, source)
}

func rawOpaqueObservations(
	values []struct {
		path  string
		value any
	},
	source string,
) []eebusraw.OpaqueObservationV1 {
	result := make([]eebusraw.OpaqueObservationV1, 0, len(values))
	for _, item := range values {
		if isNilRawRuntimeValue(item.value) {
			continue
		}
		encoded, err := json.Marshal(item.value)
		if err != nil {
			continue
		}
		value, err := eebusraw.DecodeTypedValueV1(encoded)
		if err != nil {
			continue
		}
		result = append(result, eebusraw.OpaqueObservationV1{
			Path:   item.path,
			Source: source,
			Value:  value,
		})
	}
	return result
}

func rawExactUnknownObservations(
	fields []spineapi.CorrelatedUnknownField,
) ([]eebusraw.OpaqueObservationV1, error) {
	const source = "eebus-go/executor.ExactFeatureResult.UnknownFields"
	if len(fields) > rawMetadataMaximumEntriesV1 {
		return nil, errors.New("exact unknown field count exceeds the size limit")
	}
	result := make([]eebusraw.OpaqueObservationV1, 0, len(fields))
	for _, field := range fields {
		path := strings.TrimSpace(field.Path)
		if path == "" || len(path) > 1024 || len(field.Value) > 64*1024 {
			return nil, errors.New("exact unknown field is outside the bounded contract")
		}
		value, err := eebusraw.DecodeTypedValueV1(field.Value.JSON())
		if err != nil {
			return nil, err
		}
		result = append(result, eebusraw.OpaqueObservationV1{Path: path, Source: source, Value: value})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Path < result[right].Path
	})
	return cloneRawOpaqueObservations(result), nil
}

func cloneRawProtocolMetadata(source rawProtocolMetadata) rawProtocolMetadata {
	return rawProtocolMetadata{
		request:  cloneRawOpaqueObservations(source.request),
		response: cloneRawOpaqueObservations(source.response),
	}
}

func mergeRawOpaqueObservations(
	groups ...[]eebusraw.OpaqueObservationV1,
) []eebusraw.OpaqueObservationV1 {
	size := 0
	for _, group := range groups {
		size += len(group)
	}
	result := make([]eebusraw.OpaqueObservationV1, 0, size)
	for _, group := range groups {
		result = append(result, cloneRawOpaqueObservations(group)...)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path == result[right].Path {
			return result[left].Source < result[right].Source
		}
		return result[left].Path < result[right].Path
	})
	return result
}

func cloneRawOpaqueObservations(
	source []eebusraw.OpaqueObservationV1,
) []eebusraw.OpaqueObservationV1 {
	if source == nil {
		return nil
	}
	result := make([]eebusraw.OpaqueObservationV1, len(source))
	for index := range source {
		result[index] = source[index].Clone()
	}
	return result
}

func rawCommandValue(command spinemodel.CmdType, requireNonEmpty bool) (eebusraw.TypedValueV1, error) {
	data, err := command.Data()
	if err != nil || data == nil || data.Value == nil {
		return eebusraw.TypedValueV1{}, errors.New("typed command data is unavailable")
	}
	encoded, err := json.Marshal(data.Value)
	if err != nil {
		return eebusraw.TypedValueV1{}, errors.New("typed command data cannot be encoded")
	}
	value, err := eebusraw.DecodeTypedValueV1(encoded)
	if err != nil {
		return eebusraw.TypedValueV1{}, err
	}
	if requireNonEmpty && rawTypedValueEmpty(value.Value()) {
		return eebusraw.TypedValueV1{}, errors.New("typed command data is empty")
	}
	return value, nil
}

func rawTypedValueEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func translateRawExecutorError(err error) *eebusraw.ErrorV1 {
	switch {
	case errors.Is(err, eebusraw.ErrSecretDetected):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1SecretDetected,
			"raw READ response contained secret-classified data",
			false,
			eebusraw.SourceLayerV1Decode,
		)
	case errors.Is(err, errRawRuntimeEpochMismatch):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RuntimeEpochMismatch,
			"runtime epoch changed before raw READ dispatch",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	case errors.Is(err, context.DeadlineExceeded):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Timeout,
			"raw READ timed out",
			true,
			eebusraw.SourceLayerV1SpineRoundTrip,
		)
	case errors.Is(err, context.Canceled):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Cancelled,
			"raw READ was cancelled",
			true,
			eebusraw.SourceLayerV1SpineRoundTrip,
		)
	case errors.Is(err, executor.ErrExactOperationNotSupported),
		errors.Is(err, executor.ErrExactPartialOperation):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1UnsupportedOperation,
			"exact full READ is unsupported",
			false,
			eebusraw.SourceLayerV1Executor,
		)
	}
	var binding *executor.ExactRemoteBindingError
	if errors.As(err, &binding) {
		if binding.Failure == executor.ExactRemoteBindingGenerationMismatch {
			return eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
				"exact remote connection generation changed",
				true,
				eebusraw.SourceLayerV1Runtime,
			)
		}
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"exact remote identity or address changed",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	var remoteError *spineapi.CorrelatedRemoteError
	if errors.As(err, &remoteError) {
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RemoteError,
			"remote rejected the raw READ",
			false,
			eebusraw.SourceLayerV1Remote,
		)
	}
	var protocolError *spineapi.CorrelatedProtocolError
	if errors.As(err, &protocolError) {
		return rawDecodeError()
	}
	switch {
	case errors.Is(err, executor.ErrExactTargetNotFound),
		errors.Is(err, executor.ErrExactSourceNotFound),
		errors.Is(err, executor.ErrExactRoundTripperUnavailable),
		errors.Is(err, spineapi.ErrCorrelatedRoundTripClosed):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"exact raw READ session is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	case errors.Is(err, executor.ErrExactTargetMismatch),
		errors.Is(err, executor.ErrExactSourceMismatch):
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1NotFound,
			"exact raw feature metadata changed",
			false,
			eebusraw.SourceLayerV1Executor,
		)
	default:
		return rawInternalError()
	}
}

func rawDecodeError() *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1DecodeError,
		"raw READ response contained no complete typed data",
		false,
		eebusraw.SourceLayerV1Decode,
	)
}

func rawInternalError() *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1Internal,
		"raw READ runtime failed without exposing backend material",
		false,
		eebusraw.SourceLayerV1Runtime,
	)
}

func rawFailuresRetriable(failures []eebusraw.ReadFailureV1) bool {
	for _, failure := range failures {
		if failure.Error.Retriable {
			return true
		}
	}
	return false
}

func exactRawRemoteIdentity(ski, shipID string, runtimeEpoch uint64) executor.ExactRemoteIdentity {
	digest := sha256.New()
	_, _ = digest.Write([]byte(rawRemoteIdentityV1))
	_, _ = digest.Write([]byte(strings.ToLower(strings.TrimSpace(ski))))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(strings.TrimSpace(shipID)))
	var epoch [8]byte
	binary.BigEndian.PutUint64(epoch[:], runtimeEpoch)
	_, _ = digest.Write(epoch[:])
	return executor.ExactRemoteIdentity("sha256:" + hex.EncodeToString(digest.Sum(nil)))
}

func rawFeatureLocatorKey(locator eebusraw.FeatureLocatorV1) string {
	hash, err := eebusraw.CanonicalSHA256V1(locator)
	if err != nil {
		return ""
	}
	return string(hash)
}

func rawModelFeatureAddress(
	device string,
	entity []uint64,
	feature uint64,
) spinemodel.FeatureAddressType {
	deviceAddress := spinemodel.AddressDeviceType(device)
	entityAddress := make([]spinemodel.AddressEntityType, len(entity))
	for index, part := range entity {
		entityAddress[index] = spinemodel.AddressEntityType(part)
	}
	featureAddress := spinemodel.AddressFeatureType(feature)
	return spinemodel.FeatureAddressType{
		Device:  &deviceAddress,
		Entity:  entityAddress,
		Feature: &featureAddress,
	}
}

func cloneRawFeatureAddress(source spinemodel.FeatureAddressType) spinemodel.FeatureAddressType {
	result := source
	if source.Device != nil {
		value := *source.Device
		result.Device = &value
	}
	result.Entity = append([]spinemodel.AddressEntityType(nil), source.Entity...)
	if source.Feature != nil {
		value := *source.Feature
		result.Feature = &value
	}
	return result
}

func isNilRawRuntimeValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sameRawRuntimeValue(left, right any) bool {
	if isNilRawRuntimeValue(left) || isNilRawRuntimeValue(right) {
		return false
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() || !leftValue.Type().Comparable() {
		return false
	}
	return leftValue.Interface() == rightValue.Interface()
}
