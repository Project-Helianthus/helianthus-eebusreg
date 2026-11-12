// Package eebusmutation implements the durable coordinator behind the raw eeBUS write tools
// (eebus.v1.features.data.set, eebus.v1.mutations.get and eebus.v1.mutations.rollback). It holds
// one global writer lease for writes, rollbacks, probe expiry and recovery, journals every
// MutationV1 transition to a synced write-ahead log before the side effect it permits, and on
// restart validates every record, resumes from a fresh full READ instead of resending a WRITE and
// keeps writes quarantined after a conflict. An invalid journal fails coordinator construction
// and is preserved untouched. The state machine is specified in
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
package eebusmutation
