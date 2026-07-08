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

MSP-02A defines the first public raw runtime identity contract. The contract is
read-only, redaction-safe, and deliberately excludes pairing mutation, trust
store mutation, listeners, snapshots, evidence dereference, and runtime
facades.

Future `enbility/eebus-go` integration is hidden behind `internal/...` and is
introduced only by the M3 facade spike. Public packages must not expose
`enbility`, SHIP, or SPINE types.

## Milestone Scope

The repository was bootstrapped by MSP-020. The current public contract starts
with MSP-02A, which adds raw identity shapes only.

Canonical platform ownership and doc-gate policy lives in
`helianthus-docs-ebus/docs/platform`. eeBUS-native protocol and device notes
live in `helianthus-docs-eebus`.

Local contract documents:

- `docs/raw-identity-contract.md`
- `docs/security/raw-identity-redaction-gate.md`

## Local CI

```bash
./scripts/ci_local.sh
```
