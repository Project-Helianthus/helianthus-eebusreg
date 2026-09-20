# Helianthus eeBUS Registry

`helianthus-eebusreg` is the public eeBUS-native registry and runtime boundary
for Project Helianthus. It owns protocol-native identity and qualification,
bounded native snapshots, evidence references, and the public runtime contracts
that expose those records without turning them into cross-protocol semantics.

EEBUS software is public Helianthus 0.7 scope. This repository does not depend
on private Helianthus hardware, and its public build and tests require no device,
credential, pairing material, private capture, or sibling checkout.

## Ownership Boundaries

The three public package boundaries are:

- `eebusruntime` at the module root: runtime lifecycle, native snapshots,
  pairing observations, and the bounded operator-facing contract;
- [`eebusraw`](./eebusraw): eeBUS-native identity, feature, value, failure, and
  mutation data contracts; and
- [`eebusevidence`](./eebusevidence): evidence references and deterministic
  evidence envelopes.

The surrounding repositories keep separate responsibilities:

| Layer | Owner and boundary |
| --- | --- |
| SHIP transport | [`helianthus-ship-go`](https://github.com/Project-Helianthus/helianthus-ship-go) owns its SHIP implementation. It is a temporary upstream fork/dependency, not a Helianthus product or semantic owner. |
| SPINE data model | [`helianthus-spine-go`](https://github.com/Project-Helianthus/helianthus-spine-go) owns its SPINE implementation. It is also a temporary upstream fork/dependency. |
| eeBUS integration | [`helianthus-eebus-go`](https://github.com/Project-Helianthus/helianthus-eebus-go) supplies the integration/runtime dependency over SHIP and SPINE. Its declared implementation versions do not establish the current normative eeBUS corpus. |
| Native registry and runtime contract | This repository owns native identity, qualification, evidence, snapshots, and its public runtime API. SHIP/SPINE and vendor implementation types remain behind internal boundaries. |
| Gateway composition | [`helianthus-ebusgateway`](https://github.com/Project-Helianthus/helianthus-ebusgateway) composes enabled runtime providers and public read surfaces. It does not own eeBUS identity or canonical semantic types. |
| Protocol-neutral semantics | [`helianthus-semreg`](https://github.com/Project-Helianthus/helianthus-semreg) owns canonical domain types, evaluation, selection, provenance, freshness, quality, and explicit projection loss. Projection is selective; native evidence remains available here. |
| Consumers | Portal, MCP, GraphQL, and Home Assistant present the stable contract slices they implement. Planned public EEBUS and Matter output bindings will consume promoted contracts; public scope does not imply completed integration. Consumers do not decode SPINE, qualify native evidence, or select their own competing semantic facts. |

Durable eeBUS protocol facts, device notes, architecture, public evidence, and
reverse-engineering records belong in
[`helianthus-docs-eebus`](https://github.com/Project-Helianthus/helianthus-docs-eebus).
This README is a repository summary and contributor route, not a copy of that
canonical documentation.

## Current Public Maturity

The inspected development baseline is
[`ede40b929e9c2d9cbf0de20c4844e737d5e6af68`](https://github.com/Project-Helianthus/helianthus-eebusreg/commit/ede40b929e9c2d9cbf0de20c4844e737d5e6af68).

| State | Evidence at that baseline |
| --- | --- |
| Implemented | Public runtime v1 and native runtime v2 contracts, raw/evidence packages, bounded lifecycle handling, API-boundary gates, and deterministic native snapshot behavior are present. Native runtime v2 entered `main` at [`db7d7ae`](https://github.com/Project-Helianthus/helianthus-eebusreg/commit/db7d7aeb2bd466589a74222cecb5b5abeac7a741). |
| Published module | The newest tag at the inspected baseline is [`v0.1.35`](https://github.com/Project-Helianthus/helianthus-eebusreg/tree/v0.1.35) at [`c0cd3a9`](https://github.com/Project-Helianthus/helianthus-eebusreg/commit/c0cd3a9625093f07b6d950f7563666d6b98c0b8c). It predates the native runtime v2 development baseline, so a tag or downstream module pin must not be treated as current `main`. |
| Offline-tested | Repository CI, API-surface gates, deterministic fixtures, fake peers, and replay-based interoperability checks exercise the code without a device. The clean-checkout commands below are the reproduction path. |
| Candidate or unresolved | The [native runtime v2 documentation](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/api/_candidate/msp-138-native-runtime-exposure-v1.md) is explicitly a candidate. Exact current SHIP, SPINE, and use-case revisions remain unresolved in the public source ledger, so normative mapping and conformance claims remain open. |
| Hardware-test-ready | No public record at this baseline establishes a complete exact-device procedure, integrated candidate, prerequisites, and acceptance evidence. The public G17/G19 design remains pending live validation. |
| Physically verified | **Unknown.** Offline tests, a discovered peer, a successful build, or an implementation dependency do not establish device qualification or physical verification. |

Implemented, published, offline-tested, hardware-test-ready, and physically
verified are independent states. None implies certification, conformance,
packaging, installation, or exact-device support.

## Public Source And Documentation Boundary

Use these public, immutable documentation references:

- [eeBUS normative source ledger](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/protocols/eebus-normative-source-ledger.md): exact current SHIP, SPINE, and use-case revisions are unresolved; dependency declarations are not normative-source evidence.
- [SHIP/SPINE overview and pending G17/G19 gates](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/protocols/ship-spine-overview.md): public derived architecture and the pending live boundary.
- [stable eeBUS runtime v1 reference](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/api/eebusruntime-v1/reference.md): the published v1 API record.
- [native runtime v2 candidate](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/api/_candidate/msp-138-native-runtime-exposure-v1.md): direction and publication boundary, explicitly not a stable normative record.
- [eeBUS documentation contribution policy](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/development/contributing.md): provenance classes, redaction, canonical paths, and restricted-source quarantine.

Do not copy protected specifications, schemas, tables, screenshots, or
restricted paraphrases into code, tests, issues, pull requests, or review
comments. Record independently publishable facts with their evidence and
uncertainty in the canonical docs repository.

## Reproduce From A Clean Public Checkout

### Prerequisites

- Git and public HTTPS access to GitHub and the Go module proxy;
- Go 1.22 or newer; and
- a working C toolchain for race-enabled tests, plus Bash and either `shasum`
  or `sha256sum` for complete local CI.

Docker is used only by the hosted build-container proof. No private Go module,
Git credential, eeBUS device, network discovery, pairing action, or local
workspace file is required.

Clone the exact development baseline:

```bash
git clone https://github.com/Project-Helianthus/helianthus-eebusreg.git
cd helianthus-eebusreg
git checkout --detach ede40b929e9c2d9cbf0de20c4844e737d5e6af68
git rev-parse HEAD
```

The last command must print
`ede40b929e9c2d9cbf0de20c4844e737d5e6af68`.

Build every package:

```bash
GOWORK=off GOTOOLCHAIN=local go build ./...
```

Expected result: exit status zero and no output.

### One Read-Only Native Example And One Negative Case

Run two existing synthetic tests:

```bash
GOWORK=off GOTOOLCHAIN=local go test -race . ./eebusraw \
  -run '^(TestNativeSnapshotV2PreservesNativeRuntimeAndRawPayload|TestIdentityDocumentV1RejectsMalformedDigests)$' \
  -count=1
```

Expected result: one `ok` line for the root `eebusruntime` package and one for
`eebusraw`.

[`TestNativeSnapshotV2PreservesNativeRuntimeAndRawPayload`](https://github.com/Project-Helianthus/helianthus-eebusreg/blob/ede40b929e9c2d9cbf0de20c4844e737d5e6af68/native_snapshot_v2_test.go#L9)
constructs a synthetic read-only snapshot and proves that native runtime
identity, service identity, an integer, and a floating-point value survive the
public contract without being replaced by a semantic value.

[`TestIdentityDocumentV1RejectsMalformedDigests`](https://github.com/Project-Helianthus/helianthus-eebusreg/blob/ede40b929e9c2d9cbf0de20c4844e737d5e6af68/eebusraw/identity_test.go#L434)
is the fail-closed negative case. It supplies malformed identity and evidence
digests and requires validation to reject every case. Invalid evidence cannot
be promoted into a native identity document.

These are synthetic public fixtures. They contain no device identifier,
credential, pairing material, private capture, or physical-device claim.

### Complete Local CI

```bash
./scripts/ci_local.sh
```

Expected result: terminology, toolchain/module, API-boundary, negative API
smoke, formatting, vet, build, and complete race-enabled tests all pass, ending
with successful package results. Hosted CI also runs an isolated Go container
proof and publishes an exact-source API-surface attestation.

When reproducing the hosted Go 1.22 lane with a Go 1.22 toolchain, use:

```bash
HELIANTHUS_EEBUSREG_MAX_GO_VERSION=1.22 GOTOOLCHAIN=local ./scripts/ci_local.sh
```

### Interoperability And Conformance Boundary

The repository's deterministic fake-peer and replay contract can be run with:

```bash
GOWORK=off GOTOOLCHAIN=local go test -race ./internal/eebusinteropsmoke -count=1
```

Expected result: the package reports `ok`. This is an offline interoperability
contract, not a normative eeBUS conformance or certification suite. No
normative conformance command is declared at this baseline because the exact
current source revisions remain unresolved. G17/G19 live evidence, device
access, pairing, deployment, and writes are outside these commands.

## Contributing

1. Read [`AGENTS.md`](./AGENTS.md), the canonical [eeBUS docs contribution
   policy](https://github.com/Project-Helianthus/helianthus-docs-eebus/blob/f61f95c59d7dc454b5afb7271ccac6b7ea15c7cd/development/contributing.md),
   and the [organization contribution guide](https://github.com/Project-Helianthus/.github/blob/0c09ded873cb5c8e2639957c0efad71bf6692de4/CONTRIBUTING.md).
2. Open one scoped issue, then create `issue/<number>-<slug>` from current
   `origin/main`.
3. Put native identity, qualification, evidence, and runtime-contract changes
   here. Put reusable protocol facts and evidence in `helianthus-docs-eebus`.
   Put canonical cross-protocol types in `helianthus-semreg`, composition in the
   Gateway, and presentation behavior in the consuming repository.
4. Add RED-first tests for protocol, lifecycle, persistence, recovery,
   concurrency, or safety behavior. Preserve partial valid state and fail
   closed on unknown or malformed evidence.
5. Run `git diff --check`, focused tests, and `./scripts/ci_local.sh`. State the
   documentation, interoperability, conformance, smoke, and live-action gate
   status in the pull request.
6. Resolve P0-P2 findings and obtain a fresh exact-HEAD review before squash
   merge. Triage P3/P4 explicitly.

Reads and discovery must be bounded and allowlisted. A public contract or
fixture does not authorize a live read, pairing attempt, trust change, write,
deployment, credential use, or device action.

### Security Reports

Do not put a vulnerability proof, credential, pairing material, device
identifier, private address, or capture in a public issue. If GitHub shows a
private **Report a vulnerability** control under the repository's Security
tab, use that channel. If it is unavailable, open a public issue containing
only a request for a private maintainer contact channel and no sensitive
technical detail. Ordinary non-sensitive defects may use the public issue
tracker.

At this baseline the repository publishes no separate verified security
contact or `SECURITY.md`; do not invent an address or treat a public issue as a
private channel.

## Licenses

The code in this repository is available under the [MIT License](./LICENSE).
Dependencies retain their own licenses and notices. Canonical eeBUS docs use
their declared AGPL-3.0-only implementation-policy lane or CC0-1.0 independently
publishable-facts lane; those documentation licenses do not grant rights in
third-party specifications, trademarks, patents, firmware, private captures,
or confidential material.
