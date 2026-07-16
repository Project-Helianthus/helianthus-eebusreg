package eebusfacade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"time"
)

const (
	firstTrustBackoffBase                     = 3 * time.Second
	firstTrustBackoffMaximum                  = 10 * time.Minute
	firstTrustBackoffExponentCap              = 8
	firstTrustAttemptMaximum           uint64 = 16
	firstTrustMaximumQuarantineRecords        = 128
	firstTrustQuarantineRetention             = 24 * time.Hour
	firstTrustMaximumTombstones               = 128
	firstTrustMaximumDurableReceipts          = 128
	firstTrustAnchorVersion            uint64 = 1
)

type firstTrustGenerationBinding struct {
	sequence      uint64
	filename      string
	sha256        [32]byte
	schemaVersion uint64
}

type firstTrustManifestBinding struct {
	epoch   uint64
	sha256  [32]byte
	current firstTrustGenerationBinding
	parent  *firstTrustGenerationBinding
}

type firstTrustPendingPublication struct {
	operationID          [32]byte
	operationClass       string
	storeInstance        [32]byte
	previousControlEpoch uint64
	targetControlEpoch   uint64
	previousManifest     firstTrustManifestBinding
	targetManifest       firstTrustManifestBinding
}

type firstTrustAnchorRecord struct {
	version                     uint64
	anchorIdentity              [32]byte
	storeInstance               [32]byte
	manifestGenerationHighWater uint64
	controlEpochHighWater       uint64
	pending                     *firstTrustPendingPublication
}

type firstTrustAssociationRecord struct {
	reference     [32]byte
	lineage       [32]byte
	subject       []byte
	service       string
	active        bool
	trusted       bool
	allowlisted   bool
	reconnectable bool
}

type firstTrustRevocationTombstone struct {
	associationRef      [32]byte
	revocationEpoch     uint64
	operationID         [32]byte
	effectiveGeneration firstTrustGenerationBinding
}

type firstTrustQuarantineRecord struct {
	scope            [32]byte
	reason           string
	state            string
	attemptCount     uint64
	backoffStep      uint64
	remainingDelay   time.Duration
	retentionBudget  time.Duration
	lastControlEpoch uint64
}

type firstTrustDurableReceipt struct {
	operationID    [32]byte
	operationClass string
	bindingSHA256  [32]byte
	result         string
	terminal       bool
}

type firstTrustLocalIdentityBinding struct {
	certificateChainDER [][]byte
	providerID          string
	providerVersion     uint64
	sealedBlob          []byte
	certificateSPKIHash [32]byte
	localSKI            []byte
}

type firstTrustControlRecord struct {
	storeInstance       [32]byte
	controlEpoch        uint64
	associationLineage  [32]byte
	tombstones          []firstTrustRevocationTombstone
	quarantines         []firstTrustQuarantineRecord
	receipts            []firstTrustDurableReceipt
	operationHighWater  uint64
	repairSequence      uint64
	replacementIdentity *firstTrustLocalIdentityBinding
}

type firstTrustControlView struct {
	manifest           firstTrustManifestBinding
	control            firstTrustControlRecord
	associations       []firstTrustAssociationRecord
	parentAssociations []firstTrustAssociationRecord
}

type firstTrustPreparedPublication struct {
	previous       firstTrustControlView
	target         firstTrustControlView
	operationID    [32]byte
	operationClass string
}

type firstTrustRevocationRequest struct {
	operationID            [32]byte
	associationRef         [32]byte
	associationLineage     [32]byte
	expectedGeneration     firstTrustGenerationBinding
	expectedManifestEpoch  uint64
	expectedManifestSHA256 [32]byte
	expectedControlEpoch   uint64
}

type firstTrustRepairRequest struct {
	operationID               [32]byte
	kind                      string
	scope                     [32]byte
	expectedState             string
	expectedReason            string
	expectedManifest          firstTrustManifestBinding
	expectedControlEpoch      uint64
	expectedAnchorVersion     uint64
	expectedManifestHighWater uint64
	expectedControlHighWater  uint64
	nextRepairSequence        uint64
}

type firstTrustBackoffPolicy struct {
	base           time.Duration
	exponentCap    int
	maximum        time.Duration
	attemptMaximum uint64
}

type firstTrustControlPersistence interface {
	ReloadControl(context.Context) (firstTrustControlView, string)
	SelectedGeneration() uint64
	PrepareControl(context.Context, firstTrustControlView, firstTrustControlRecord, [32]byte, string) (firstTrustPreparedPublication, string)
	CommitControl(context.Context, firstTrustPreparedPublication) string
	ObserveControlPublication(context.Context, firstTrustPendingPublication) string
}

type firstTrustAnchorProvider interface {
	Open(context.Context) (firstTrustAnchorRecord, string)
	CompareAndStage(context.Context, firstTrustAnchorRecord, firstTrustPendingPublication) string
	CompareAndFinalize(context.Context, firstTrustPendingPublication) string
	CompareAndClear(context.Context, firstTrustPendingPublication) string
	Create(context.Context, uint64, [32]byte) (firstTrustAnchorRecord, string)
}

type firstTrustIdentityProvider interface {
	CreateSigningIdentity(context.Context) (firstTrustLocalIdentityBinding, string)
}

type firstTrustRecoveryOperation struct {
	operationID    [32]byte
	operationClass string
	bindingSHA256  [32]byte
}

type firstTrustRetryArm struct {
	deadline time.Duration
	armedAt  time.Duration
}

func newFirstTrustCoordinatorWithRecovery(
	wallNow func() time.Time,
	monotonicNow func() time.Duration,
	random io.Reader,
	store firstTrustControlPersistence,
	anchor firstTrustAnchorProvider,
	effects firstTrustEffects,
	policy firstTrustBackoffPolicy,
) *firstTrustCoordinator {
	coordinator := newFirstTrustCoordinator(wallNow, random, nil, effects)
	if monotonicNow == nil {
		origin := time.Now()
		monotonicNow = func() time.Duration { return time.Since(origin) }
	}
	coordinator.monotonicNow = monotonicNow
	coordinator.recoveryStore = store
	coordinator.anchor = anchor
	coordinator.backoffPolicy = policy
	coordinator.recovery = "NO_LOCAL_IDENTITY"
	coordinator.retryArms = make(map[[32]byte]firstTrustRetryArm)
	coordinator.retryInflight = make(map[[32]byte]bool)
	return coordinator
}

func firstTrustProductAllowed(phase, recovery string) bool {
	switch phase {
	case "DISABLED":
		return recovery == "NO_LOCAL_IDENTITY" || recovery == "QUARANTINED" || recovery == "CORRUPT_STORE"
	case "PAIRING_CLOSED":
		return recovery == "UNPAIRED_LOCKED" || recovery == "PAIRED_TRUSTED" || recovery == "REVOKED"
	case "OPEN_EMPTY", "CANDIDATE_PENDING", "COMMITTING":
		return recovery == "UNPAIRED_LOCKED" || recovery == "REVOKED"
	default:
		return false
	}
}

func normalizeFirstTrustProduct(phase, recovery, structural string) (string, string) {
	if firstTrustProductAllowed(phase, recovery) {
		return phase, recovery
	}
	if structural == "CORRUPT_STORE" {
		return "DISABLED", "CORRUPT_STORE"
	}
	return "DISABLED", "QUARANTINED"
}

func firstTrustRecoveryTransitionAllowed(from, to string) bool {
	switch from {
	case "NO_LOCAL_IDENTITY":
		return to == "UNPAIRED_LOCKED" || to == "QUARANTINED"
	case "UNPAIRED_LOCKED":
		return to == "PAIRED_TRUSTED" || to == "QUARANTINED" || to == "CORRUPT_STORE"
	case "PAIRED_TRUSTED":
		return to == "REVOKED" || to == "QUARANTINED" || to == "CORRUPT_STORE"
	case "REVOKED":
		return to == "PAIRED_TRUSTED" || to == "QUARANTINED"
	case "QUARANTINED":
		return to == "UNPAIRED_LOCKED" || to == "REVOKED" || to == "CORRUPT_STORE"
	case "CORRUPT_STORE":
		return to == "UNPAIRED_LOCKED" || to == "REVOKED" || to == "QUARANTINED"
	default:
		return false
	}
}

func firstTrustPendingPublicationEqual(left, right firstTrustPendingPublication) bool {
	return left.operationID == right.operationID && left.operationClass == right.operationClass &&
		left.storeInstance == right.storeInstance && left.previousControlEpoch == right.previousControlEpoch &&
		left.targetControlEpoch == right.targetControlEpoch && firstTrustManifestEqual(left.previousManifest, right.previousManifest) &&
		firstTrustManifestEqual(left.targetManifest, right.targetManifest)
}

func firstTrustAnchorRecordEqual(left, right firstTrustAnchorRecord) bool {
	if left.version != right.version || left.anchorIdentity != right.anchorIdentity || left.storeInstance != right.storeInstance ||
		left.manifestGenerationHighWater != right.manifestGenerationHighWater || left.controlEpochHighWater != right.controlEpochHighWater ||
		(left.pending == nil) != (right.pending == nil) {
		return false
	}
	return left.pending == nil || firstTrustPendingPublicationEqual(*left.pending, *right.pending)
}

func firstTrustManifestEqual(left, right firstTrustManifestBinding) bool {
	if left.epoch != right.epoch || left.sha256 != right.sha256 || left.current != right.current || (left.parent == nil) != (right.parent == nil) {
		return false
	}
	return left.parent == nil || *left.parent == *right.parent
}

func cloneFirstTrustAnchorRecord(source firstTrustAnchorRecord) firstTrustAnchorRecord {
	result := source
	if source.pending != nil {
		pending := cloneFirstTrustPendingPublication(*source.pending)
		result.pending = &pending
	}
	return result
}

func cloneFirstTrustPendingPublication(source firstTrustPendingPublication) firstTrustPendingPublication {
	result := source
	result.previousManifest = cloneFirstTrustManifest(source.previousManifest)
	result.targetManifest = cloneFirstTrustManifest(source.targetManifest)
	return result
}

func cloneFirstTrustManifest(source firstTrustManifestBinding) firstTrustManifestBinding {
	result := source
	if source.parent != nil {
		parent := *source.parent
		result.parent = &parent
	}
	return result
}

func cloneFirstTrustControlView(source firstTrustControlView) firstTrustControlView {
	result := source
	result.manifest = cloneFirstTrustManifest(source.manifest)
	result.control = cloneFirstTrustControlRecord(source.control)
	result.associations = cloneFirstTrustAssociations(source.associations)
	result.parentAssociations = cloneFirstTrustAssociations(source.parentAssociations)
	return result
}

func cloneFirstTrustControlRecord(source firstTrustControlRecord) firstTrustControlRecord {
	result := source
	result.tombstones = append([]firstTrustRevocationTombstone(nil), source.tombstones...)
	result.quarantines = append([]firstTrustQuarantineRecord(nil), source.quarantines...)
	result.receipts = append([]firstTrustDurableReceipt(nil), source.receipts...)
	if source.replacementIdentity != nil {
		identity := cloneFirstTrustLocalIdentityBinding(*source.replacementIdentity)
		result.replacementIdentity = &identity
	}
	return result
}

func cloneFirstTrustLocalIdentityBinding(source firstTrustLocalIdentityBinding) firstTrustLocalIdentityBinding {
	result := source
	result.certificateChainDER = make([][]byte, len(source.certificateChainDER))
	for index, certificate := range source.certificateChainDER {
		result.certificateChainDER[index] = bytes.Clone(certificate)
	}
	result.sealedBlob = bytes.Clone(source.sealedBlob)
	result.localSKI = bytes.Clone(source.localSKI)
	return result
}

func cloneFirstTrustAssociations(source []firstTrustAssociationRecord) []firstTrustAssociationRecord {
	result := make([]firstTrustAssociationRecord, len(source))
	for index, association := range source {
		result[index] = association
		result[index].subject = bytes.Clone(association.subject)
	}
	return result
}

func firstTrustNextBackoff(policy firstTrustBackoffPolicy, current uint64) (uint64, time.Duration, bool) {
	if policy.base <= 0 || policy.maximum < policy.base || policy.exponentCap < 0 || policy.attemptMaximum == 0 || current > policy.attemptMaximum {
		return 0, 0, false
	}
	next := current
	if next < policy.attemptMaximum {
		next++
	}
	if next == 0 {
		return 0, 0, false
	}
	exponent := next - 1
	if exponent > uint64(policy.exponentCap) {
		exponent = uint64(policy.exponentCap)
	}
	delay := policy.base
	for step := uint64(0); step < exponent; step++ {
		if delay >= policy.maximum || delay > time.Duration(math.MaxInt64/2) || delay > policy.maximum/2 {
			delay = policy.maximum
			break
		}
		delay *= 2
	}
	if delay > policy.maximum {
		delay = policy.maximum
	}
	return next, delay, true
}

func firstTrustPendingFromPrepared(publication firstTrustPreparedPublication) firstTrustPendingPublication {
	storeInstance := publication.previous.control.storeInstance
	if publication.operationClass == "recover_unavailable_host_key" {
		storeInstance = publication.target.control.storeInstance
	}
	return firstTrustPendingPublication{
		operationID: publication.operationID, operationClass: publication.operationClass,
		storeInstance:        storeInstance,
		previousControlEpoch: publication.previous.control.controlEpoch,
		targetControlEpoch:   publication.target.control.controlEpoch,
		previousManifest:     cloneFirstTrustManifest(publication.previous.manifest),
		targetManifest:       cloneFirstTrustManifest(publication.target.manifest),
	}
}

func firstTrustReadOrdinal(reader io.Reader) ([32]byte, bool) {
	var result [32]byte
	if reader == nil {
		return result, false
	}
	_, err := io.ReadFull(reader, result[:])
	return result, err == nil && result != [32]byte{}
}

func firstTrustOperationOrdinal(value [32]byte) uint64 {
	return binary.BigEndian.Uint64(value[len(value)-8:])
}

func firstTrustHashRevocation(request firstTrustRevocationRequest) [32]byte {
	hash := sha256.New()
	hash.Write(request.operationID[:])
	hash.Write(request.associationRef[:])
	hash.Write(request.associationLineage[:])
	firstTrustWriteGenerationHash(hash, request.expectedGeneration)
	firstTrustWriteUint64(hash, request.expectedManifestEpoch)
	hash.Write(request.expectedManifestSHA256[:])
	firstTrustWriteUint64(hash, request.expectedControlEpoch)
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func firstTrustHashRepair(request firstTrustRepairRequest) [32]byte {
	hash := sha256.New()
	hash.Write(request.operationID[:])
	firstTrustWriteStringHash(hash, request.kind)
	hash.Write(request.scope[:])
	firstTrustWriteStringHash(hash, request.expectedState)
	firstTrustWriteStringHash(hash, request.expectedReason)
	firstTrustWriteManifestHash(hash, request.expectedManifest)
	firstTrustWriteUint64(hash, request.expectedControlEpoch)
	firstTrustWriteUint64(hash, request.expectedAnchorVersion)
	firstTrustWriteUint64(hash, request.expectedManifestHighWater)
	firstTrustWriteUint64(hash, request.expectedControlHighWater)
	firstTrustWriteUint64(hash, request.nextRepairSequence)
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func firstTrustWriteManifestHash(writer io.Writer, manifest firstTrustManifestBinding) {
	firstTrustWriteUint64(writer, manifest.epoch)
	_, _ = writer.Write(manifest.sha256[:])
	firstTrustWriteGenerationHash(writer, manifest.current)
	if manifest.parent == nil {
		firstTrustWriteUint64(writer, 0)
		return
	}
	firstTrustWriteUint64(writer, 1)
	firstTrustWriteGenerationHash(writer, *manifest.parent)
}

func firstTrustWriteGenerationHash(writer io.Writer, generation firstTrustGenerationBinding) {
	firstTrustWriteUint64(writer, generation.sequence)
	firstTrustWriteStringHash(writer, generation.filename)
	_, _ = writer.Write(generation.sha256[:])
	firstTrustWriteUint64(writer, generation.schemaVersion)
}

func firstTrustWriteStringHash(writer io.Writer, value string) {
	firstTrustWriteUint64(writer, uint64(len(value)))
	_, _ = io.WriteString(writer, value)
}

func firstTrustWriteUint64(writer io.Writer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}
