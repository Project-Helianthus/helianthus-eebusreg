# helianthus-eebusreg

Raw eeBUS runtime and evidence contracts for Helianthus.

This repository intentionally starts as a narrow contract shell. Alongside the
raw contract packages, it now contains an internal `enbility/eebus-go` facade
spike; it does not contain a listener, pairing implementation,
trust-store implementation, gateway adapter, GraphQL surface, Portal surface,
Home Assistant surface, command routing, raw writes, or promoted facts.

## Public Boundary

Reserved public package names:

- `eebusruntime`
- `eebusraw`
- `eebusevidence`

MSP-02A defines the first public raw runtime identity contract. MSP-02B defines
the raw snapshot/evidence envelope contract. Both contracts are read-only and
redaction-safe; they deliberately exclude pairing mutation, trust store
mutation, listeners, runtime capture, evidence dereference, and runtime
facades.

The `enbility/eebus-go` integration is hidden behind `internal/...` and starts
as the MSP-03A facade spike pinned to `github.com/enbility/eebus-go v0.7.0`.
Public packages must not expose `enbility`, SHIP, or SPINE types.

## Milestone Scope

The repository was bootstrapped by MSP-020. The current public contracts cover
raw identity plus raw snapshot/evidence envelope descriptors only. MSP-03A adds
an internal-only compile-time facade proof for `eebus-go/api`; it does not start
the runtime or add trust, pairing, MCP, gateway, or consumer behavior.

Canonical platform ownership and doc-gate policy lives in
`helianthus-docs-ebus/docs/platform`. eeBUS-native protocol and device notes
live in `helianthus-docs-eebus`.

Local contract documents:

- `docs/raw-identity-contract.md`
- `docs/security/raw-identity-redaction-gate.md`
- `docs/snapshot-envelope-evidence.md`
- `docs/internal-facade-spike.md`
- `docs/toolchain-boundary-proof.md`

## Local CI

```bash
./scripts/ci_local.sh
```
