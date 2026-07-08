# helianthus-eebusreg

Raw eeBUS runtime and evidence contracts for Helianthus.

This repository intentionally starts as a narrow contract shell. It does not
contain an `enbility/eebus-go` facade, listener, pairing implementation,
trust-store implementation, gateway adapter, GraphQL surface, Portal surface,
Home Assistant surface, command routing, raw writes, or promoted facts.

## Public Boundary

Reserved public package names:

- `eebusruntime`
- `eebusraw`
- `eebusevidence`

MSP-020 does not define public runtime identity, pairing, trust, snapshot, or
evidence reference contracts. Those shapes are deliberately deferred to later
gated issues.

Future `enbility/eebus-go` integration is hidden behind `internal/...` and is
introduced only by the M3 facade spike. Public packages must not expose
`enbility`, SHIP, or SPINE types.

## Milestone Scope

This bootstrap corresponds to MSP-020 in the eeBUS VR940f raw-first execution
plan. It creates the repo boundary needed before MSP-02A can draft the raw
runtime identity contract.

Canonical platform ownership and doc-gate policy lives in
`helianthus-docs-ebus/docs/platform`. eeBUS-native protocol and device notes
live in `helianthus-docs-eebus`.

## Local CI

```bash
./scripts/ci_local.sh
```
