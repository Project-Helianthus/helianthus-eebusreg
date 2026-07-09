# eeBUS Interop Smoke Harness

Status: `MSP-03D`

Issue: Project-Helianthus/helianthus-eebusreg#12

Canonical platform gate:
`helianthus-docs-ebus/docs/platform/eebus-interop-smoke.md`, merged in
Project-Helianthus/helianthus-docs-ebus#340 at
`114072fe8bdf027cfdd3472d7f2b0896a2496db4`.

## Scope

`internal/eebusinteropsmoke` is an evidence-only harness for
`eebus-transport-gate v0` cases:

- `EEBUS-G01` black-box fake peer handshake;
- `EEBUS-G17` live VR940f smoke readiness and blocked-state reporting.

The harness is internal. It does not add public runtime APIs, gateway imports,
MCP tools, production trust-store persistence, command routing, raw writes, or
candidate semantic facts.

## Modes

Fake peer:

```bash
GOWORK=off go run ./internal/eebusinteropsmoke -mode fake-peer -timeout 30s
```

This mode creates two disposable in-memory certificates and two local
`eebus-go v0.7.0` services. The peer side imports no Helianthus facade under
test. The report is public-redacted and contains only evidence labels plus
digest refs.

Live VR940f readiness:

```bash
GOWORK=off go run ./internal/eebusinteropsmoke -mode live-vr940f -timeout 10s
```

This mode probes for `_ship._tcp` DNS-SD visibility. If no live SHIP service is
visible, or if an approved remote SKI is not supplied, it returns `BLOCKED`.
It must not be converted into a PASS without real discovery, pairing/session,
feature graph, and reconnect evidence.

## Redaction

Public reports must not contain:

- raw SKI, certificate fingerprint, peer id, session id, or pairing history;
- raw IP, MAC, serial, private key, token, or PEM material;
- production trust-store paths or state.

The harness validates its own JSON report before printing it.

## Non-Scope

The harness does not freeze a raw runtime contract. MSP-03D remains M3
feasibility evidence. MSP-03D fake-peer success is not sufficient for gateway
import, M3.5 contract freeze, or M4 trust work.
