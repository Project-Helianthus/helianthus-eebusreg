// Package eebusruntime is the public runtime boundary of the Helianthus eeBUS registry; its
// import path is the module root.
//
// It owns the runtime lifecycle (New, NewOperatorRuntimeV1 and NewNativeRuntimeV2), the bounded
// snapshots a running runtime publishes (the raw SnapshotV1, its masked RedactedSnapshotV1 and
// the NativeSnapshotV2), the operator administration surface AdminV1, the raw feature read and
// governed write surfaces RawFeatureRuntimeV1 and RawMutationRuntimeV1, and the participation
// contract a gateway driver implements (ParticipationConfigurationV1 and ParticipationDriverV1).
// Constructors validate their configuration before any I/O, validators fail closed on unknown
// values and exceeded bounds, and accessors return deep copies that the caller owns.
//
// Two public sibling packages hold the shared data contracts: eebusraw defines the eeBUS-native
// identity, feature, value, error and mutation records the raw surfaces carry, and eebusevidence
// defines the evidence references and envelopes that participation provenance retains. The
// protocol stack sits behind an internal facade, so no SHIP or SPINE type crosses this API. The
// package records native observations as evidence and does not interpret them into
// cross-protocol semantics, which belong to their respective owners.
//
// Protocol and API semantics are documented at
// https://github.com/Project-Helianthus/helianthus-docs-eebus.
package eebusruntime
