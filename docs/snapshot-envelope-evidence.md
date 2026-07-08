# Snapshot Envelope And Evidence Object

Status: `MSP-02B`

Canonical platform gate:
`helianthus-docs-ebus/docs/platform/eebus-raw-first-contract.md`, merged in
Project-Helianthus/helianthus-docs-ebus#334 at
`55f5482e0513ceb3bed8ddd5f2656d3b3ae7be41`.

## Scope

The first public raw envelope contract is
`helianthus.eebus.raw.evidence-envelope.v1alpha1`.

It covers:

- reference binding to runtime, contract, tool, scope, mask tier, and effective
  auth scope;
- immutable evidence object descriptors;
- deterministic data hash inputs for replay comparison.

It does not cover:

- live runtime capture;
- evidence dereference;
- MCP server implementation;
- trust-store persistence;
- gateway sidecar integration;
- GraphQL, Portal, Home Assistant, command routing, raw writes, or promoted
  facts.

## Reference Binding

`Reference` binds:

- `runtime`: `eebusraw.RedactedID` with required redacted digest;
- `contract`: `helianthus.eebus.raw.evidence-envelope.v1alpha1`;
- `tool`: allowlisted raw MCP tool id;
- `scope`: allowlisted static scope;
- `mask_tier`: `redacted`;
- `auth_scope`: `eebus.raw.read`.

All labels are allowlists. Caller-controlled labels are rejected so raw SKI,
fingerprint, peer, session, or payload bytes cannot leak through metadata.

## Evidence Object

`Object` descriptors contain:

- allowlisted object kind;
- lowercase `sha256:<hex>` digest;
- byte size;
- `data_timestamp`;
- optional redacted `eebusraw.UnknownField` entries.

Descriptors do not contain raw payload bytes. Raw payloads may be used only as
intake to compute a digest and size; callers must not log or persist the raw
argument before conversion.

## Hash Inputs

`Envelope.ComputeDataHash` hashes:

- reference binding;
- `data_timestamp`, normalized to UTC;
- object descriptors sorted by kind, digest, data timestamp, size, and
  redacted unknown-field material.

The hash material is encoded as restricted RFC 8785 canonical JSON: static
lexicographic field order, no caller-provided map keys, no insignificant
whitespace, UTC RFC3339 timestamps, decimal integer sizes, and lowercase digest
strings only. This keeps replay hashes portable across Go versions and
construction paths while this milestone still has no live capture or
dereference implementation.

It intentionally excludes:

- `captured_at`;
- `data_hash`;
- live runtime state;
- dereferenced payload bytes.

If an envelope carries `data_hash`, validation recomputes the hash and rejects
stale or forged values before public JSON serialization.
