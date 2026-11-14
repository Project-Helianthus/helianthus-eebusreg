package eebusruntime

import (
	"time"
	"unicode/utf8"
)

// MutationPreconditionV1 is embedded in every mutation request. IdempotencyKey is 1 to 128
// bytes of valid UTF-8. Reusing a key with the same request and expected revision returns the
// recorded result, with Replayed set, or the recorded failure, without a second effect. At most
// 128 records are kept, and only successful records of older revisions are evicted. Reusing a
// key with a different request fails with AdminErrorCodeV1IdempotencyConflict. ExpectedStateRevision
// is nonzero and must equal the revision after facts are re-read, otherwise the mutation fails
// with AdminErrorCodeV1StateConflict.
type MutationPreconditionV1 struct {
	IdempotencyKey        string
	ExpectedStateRevision uint64
}

func validateOperatorAdminV1Precondition(precondition MutationPreconditionV1) *AdminErrorV1 {
	if len(precondition.IdempotencyKey) < 1 || len(precondition.IdempotencyKey) > 128 ||
		!utf8.ValidString(precondition.IdempotencyKey) || precondition.ExpectedStateRevision == 0 {
		return operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	return nil
}

func validateOperatorAdminV1Facts(facts operatorAdminV1SnapshotFacts) *AdminErrorV1 {
	if facts.capturedAt.IsZero() || !validOperatorAdminV1SKI(facts.localSKI) ||
		facts.localSHIPID == "" || !validOperatorAdminV1OptionalText(facts.localSHIPID) ||
		!validOperatorAdminV1Status(facts.status) ||
		!validOperatorAdminV1Status(facts.window) || !validOperatorAdminV1Status(facts.register) ||
		!validOperatorAdminV1Status(facts.listener) || !validOperatorAdminV1Status(facts.discovery) ||
		facts.window == "open" && facts.windowDeadline.IsZero() || !validOperatorAdminV1DegradedCode(facts.degraded) {
		return newAdminBoundaryUnavailableV1()
	}
	if action := facts.activeAction; action != nil &&
		(!validOperatorAdminV1ActionID(action.actionID) || action.kind != "connect" ||
			(action.state != "pending" && action.state != "terminal") || action.expiresAt.IsZero() ||
			!facts.capturedAt.Before(action.expiresAt) || action.state == "pending" && action.outcome != "" ||
			action.state == "terminal" && !validOperatorAdminV1ActionOutcome(action.outcome)) {
		return newAdminBoundaryUnavailableV1()
	}
	if len(facts.trusted) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.connected) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.discovered) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.candidates) > operatorAdminV1MaximumHandlesPerKind {
		return newAdminBoundaryUnavailableV1()
	}
	seenTrusted := make(map[string]struct{}, len(facts.trusted))
	for _, fact := range facts.trusted {
		if !validOperatorAdminV1Reference(fact.reference) || !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Endpoint(fact.endpoint) || !validOperatorAdminV1Status(fact.trustState) ||
			!validOperatorAdminV1Status(fact.connectionState) || !validOperatorAdminV1OptionalText(fact.shipID) ||
			!validOperatorAdminV1OptionalText(fact.name) || !validOperatorAdminV1OptionalText(fact.identifier) ||
			!validOperatorAdminV1OptionalText(fact.brand) || !validOperatorAdminV1OptionalText(fact.typeName) ||
			!validOperatorAdminV1OptionalText(fact.model) || !validOperatorAdminV1RetryFact(facts.capturedAt, fact) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenTrusted[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenTrusted[fact.reference] = struct{}{}
	}
	for _, fact := range facts.connected {
		if !validOperatorAdminV1SKI(fact.ski) || !validOperatorAdminV1Endpoint(fact.endpoint) ||
			!validOperatorAdminV1Status(fact.connectionState) || !validOperatorAdminV1Status(fact.trustState) ||
			!validOperatorAdminV1OptionalText(fact.shipID) || !validOperatorAdminV1OptionalText(fact.name) ||
			!validOperatorAdminV1OptionalText(fact.identifier) || !validOperatorAdminV1OptionalText(fact.brand) ||
			!validOperatorAdminV1OptionalText(fact.typeName) || !validOperatorAdminV1OptionalText(fact.model) {
			return newAdminBoundaryUnavailableV1()
		}
	}
	seenDiscovered := make(map[string]struct{}, len(facts.discovered))
	for _, fact := range facts.discovered {
		if !validOperatorAdminV1Reference(fact.reference) || fact.ski != "" && !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Endpoint(fact.endpoint) || fact.observationRevision == 0 ||
			!validOperatorAdminV1OptionalText(fact.name) || !validOperatorAdminV1OptionalText(fact.identifier) ||
			!validOperatorAdminV1OptionalText(fact.brand) || !validOperatorAdminV1OptionalText(fact.typeName) ||
			!validOperatorAdminV1OptionalText(fact.model) || !fact.expiresAt.IsZero() && !facts.capturedAt.Before(fact.expiresAt) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenDiscovered[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenDiscovered[fact.reference] = struct{}{}
	}
	seenCandidates := make(map[string]struct{}, len(facts.candidates))
	for _, fact := range facts.candidates {
		if !validOperatorAdminV1Reference(fact.reference) || !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Status(fact.state) || fact.expiresAt.IsZero() || !facts.capturedAt.Before(fact.expiresAt) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenCandidates[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenCandidates[fact.reference] = struct{}{}
	}
	return nil
}

func validOperatorAdminV1RetryFact(capturedAt time.Time, fact operatorAdminV1TrustedFact) bool {
	switch fact.retryState {
	case "":
		return fact.retryDeadline.IsZero() && !fact.retryAdmitted
	case "RETRY_READY":
		return fact.retryDeadline.IsZero()
	case "BACKOFF_ACTIVE":
		return !fact.retryDeadline.IsZero() && !fact.retryDeadline.Before(capturedAt) && !fact.retryAdmitted
	case "ADMIN_HOLD":
		return fact.retryDeadline.IsZero() && !fact.retryAdmitted
	default:
		return false
	}
}

func validOperatorAdminV1Status(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && utf8.ValidString(value)
}

func validOperatorAdminV1OptionalText(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value)
}

func validOperatorAdminV1DegradedCode(value AdminErrorCodeV1) bool {
	if value == "" {
		return true
	}
	return normalizeOperatorAdminV1Error(operatorAdminV1Error(value)).Code == value
}

func validOperatorAdminV1Reference(value string) bool {
	return len(value) >= 1 && len(value) <= operatorAdminV1MaximumReferenceBytes && utf8.ValidString(value)
}

func validOperatorAdminV1Endpoint(value string) bool {
	return len(value) <= operatorAdminV1MaximumEndpointBytes && utf8.ValidString(value)
}

func validOperatorAdminV1SKI(value string) bool {
	if len(value) != 40 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}
	return true
}

func validOperatorAdminV1PIN(value []byte) bool {
	if len(value) < 8 || len(value) > 16 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

func validOperatorAdminV1ActionID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}
	return true
}

func validOperatorAdminV1ActionOutcome(value AdminOutcomeV1) bool {
	switch value {
	case "connection_completed", "attempt_timeout", "disconnected", "trust_denied", "unknown_state",
		"pin_required", "pin_optional", "pin_busy", "pin_rejected", "pin_unavailable", "pin_protocol_error":
		return true
	default:
		return false
	}
}

func validOperatorAdminV1View(view AdminViewV1) bool {
	switch view {
	case AdminViewV1Trusted, AdminViewV1Connected, AdminViewV1Discovered, AdminViewV1Candidate:
		return true
	default:
		return false
	}
}

func cloneOperatorAdminV1Facts(source operatorAdminV1SnapshotFacts) operatorAdminV1SnapshotFacts {
	result := source
	result.trusted = append([]operatorAdminV1TrustedFact(nil), source.trusted...)
	result.connected = append([]operatorAdminV1ConnectedFact(nil), source.connected...)
	result.discovered = append([]operatorAdminV1DiscoveredFact(nil), source.discovered...)
	result.candidates = append([]operatorAdminV1CandidateFact(nil), source.candidates...)
	if source.activeAction != nil {
		action := *source.activeAction
		result.activeAction = &action
	}
	return result
}

func equalOperatorAdminV1Facts(left, right operatorAdminV1SnapshotFacts) bool {
	if left.status != right.status || left.window != right.window || !left.windowDeadline.Equal(right.windowDeadline) ||
		left.localSKI != right.localSKI || left.localSHIPID != right.localSHIPID ||
		left.register != right.register || left.listener != right.listener || left.discovery != right.discovery ||
		left.degraded != right.degraded || !equalOperatorAdminV1ActiveActionFacts(left.activeAction, right.activeAction) {
		return false
	}
	if len(left.trusted) != len(right.trusted) || len(left.connected) != len(right.connected) ||
		len(left.discovered) != len(right.discovered) || len(left.candidates) != len(right.candidates) {
		return false
	}
	for index := range left.trusted {
		if left.trusted[index] != right.trusted[index] {
			return false
		}
	}
	for index := range left.connected {
		if left.connected[index] != right.connected[index] {
			return false
		}
	}
	for index := range left.discovered {
		if left.discovered[index] != right.discovered[index] {
			return false
		}
	}
	for index := range left.candidates {
		if left.candidates[index] != right.candidates[index] {
			return false
		}
	}
	return true
}

func equalOperatorAdminV1ActiveActionFacts(left, right *operatorAdminV1ActiveActionFact) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.actionID == right.actionID && left.kind == right.kind && left.state == right.state &&
		left.outcome == right.outcome && left.retryable == right.retryable && left.expiresAt.Equal(right.expiresAt)
}
