# Internal eebus-go Facade Spike

Status: `MSP-03A`

Issue: Project-Helianthus/helianthus-eebusreg#8

## Scope

This spike pins `github.com/enbility/eebus-go v0.7.0` and proves Helianthus can
compile against the upstream `api` package behind an `internal/...` boundary.
The dependency is treated as a SHIP/SPINE runtime facade, not as a byte
transport and not as a public Helianthus registry API.

The only implementation package added by this spike is
`internal/eebusfacade`. It records deterministic evidence for:

- the exact upstream module path and version;
- the minimal `github.com/enbility/eebus-go/api` import;
- the approved read-only service, pairing-observation, service-discovery, and
  configuration-reader methods;
- the upstream runtime lifecycle and pairing mutator methods that are
  explicitly excluded from MSP-03A;
- the absence of gateway imports, listener setup, trust-store persistence, or
  public type leakage.

## Non-Scope

MSP-03A does not add:

- service setup or service start;
- listeners, mDNS wiring, interface binding, or manual endpoints;
- certificate generation, certificate loading, trust-store persistence, or
  pairing approval flows;
- gateway sidecar imports;
- MCP tools, evidence capture, GraphQL, Portal, Home Assistant, command
  routing, raw writes, or candidate semantics.

## Boundary Rules

Public packages remain limited to `eebusruntime`, `eebusraw`, and
`eebusevidence`. Direct `github.com/enbility/...` imports are allowed only under
`internal/`.

The facade exports only plain internal evidence structs. It must not expose
upstream eeBUS, SHIP, or SPINE types through exported facade signatures. The
compile-time probe that binds selected upstream symbols is intentionally
unexported.

`Start`, `Shutdown`, `RegisterRemoteSKI`, `UnregisterRemoteSKI`,
`SetAutoAccept`, and related lifecycle or pairing methods are not approved
Helianthus facade calls in this milestone. They are tracked only as excluded
upstream hazards until the later runtime, trust, and admin-gate issues define
safe behavior.

## Verification

Required local checks:

```bash
GOWORK=off go list -m -json github.com/enbility/eebus-go
! git grep -n 'github.com/enbility' -- '*.go' ':!internal/**'
GOWORK=off go mod tidy -diff
GOWORK=off go mod verify
GOWORK=off go list -m all
./scripts/ci_local.sh
git diff --check
```

The M3.5 raw identity freeze and later M4 trust-store work must not reuse this
spike as an authorization to start the upstream service. Runtime start,
pairing mutation, trust persistence, and gateway integration each require their
own issue, acceptance criteria, and doc gate.
