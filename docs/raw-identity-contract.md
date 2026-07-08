# Raw Runtime Identity Contract

Status: `MSP-02A`

Canonical platform gate:
`helianthus-docs-ebus/docs/platform/eebus-raw-first-contract.md`, merged in
Project-Helianthus/helianthus-docs-ebus#334 at
`55f5482e0513ceb3bed8ddd5f2656d3b3ae7be41`.

## Scope

The first public raw identity contract is
`helianthus.eebus.raw.identity.v1alpha1`.

It covers:

- local endpoint identity as a redacted stable identifier with an allowlisted
  `IDKind`;
- remote endpoint identities as redacted stable identifiers with allowlisted
  `IDKind` values;
- observed pairing state as read-only data;
- observed session identity as redacted data;
- unknown eeBUS and SPINE values as opaque redacted evidence attached only to
  static, allowlisted `UnknownPath` values.

It explicitly does not cover:

- pairing or trust mutation;
- pairing-window control;
- listener or network runtime setup;
- trust-store persistence;
- snapshots, hashes, or evidence dereference;
- gateway, GraphQL, Portal, Home Assistant, command routing, or promoted facts.

## Redaction Rule

Stable identifiers such as SKI values, fingerprints, session identifiers, and
peer identifiers must not appear unmasked in public values, JSON output, logs,
tests, issues, or docs. Public values expose only:

- `masked: "[redacted]"`;
- optional `sha256:<hex>` redacted hashes;
- byte size for opaque unknown values.

Unknown fields remain unknown. They are carried as `UnknownField` plus
`OpaqueValue`, not normalized into interpreted consumer meaning. `IDKind` and
`UnknownPath` are allowlists so caller-provided labels cannot become a
side-channel for raw SKI, fingerprint, peer, session, or unknown-value bytes.
