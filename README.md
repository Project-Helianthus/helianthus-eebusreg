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

## Portal Participation Contract (PORTAL-05)

Issue [#137](https://github.com/Project-Helianthus/helianthus-eebusreg/issues/137)
adds the typed `eebusruntime` contract
[`ParticipationContractV1`](./participation_contract_v1.go), with pure validators
and the [`ParticipationDriverV1`](./participation_driver_v1.go) extension hook.
This is a contract publication in source, not an implemented Portal workflow,
runtime adapter, binding executor, persistence backend, or physical qualification.
Gateway PORTAL-03 consumes the hook after its generic configuration foundations.
It must not decode SHIP/SPINE or bypass accepted semantic producers/bindings.

The runtime boundary remains this repository's responsibility; its internal
facade composes `helianthus-eebus-go` and the SHIP/SPINE dependencies. Gateway
owns Portal composition, HTTP authorization, versioned atomic configuration
storage, edit conflicts and package restart coordination. The native owner
keeps protected participant identity, peer trust, negotiated permissions and
native evidence; the existing semantic producer owns promotion/selection and
projection assessment. This contract references that producer's selected result;
it neither defines universal semantic types nor replaces accepted consumer work.
No implementation of `ParticipationDriverV1` is supplied by existing constructors.
Canonical API documentation still belongs in `helianthus-docs-eebus`: publish the
matching reference there and reconcile the accepted producer/binding adapter
before Gateway integration merges. This source contract and its fixture tests
do not close that documentation or integration gate.

### Typed Contribution And Ordinary Workflow

`ParticipationConfigurationV1` contains a version, monotonically increasing
saved revision, stable local participant reference, enable flag, one explicit
listener (interface and unicast address/port), discovery preference and selected
`ParticipationBindingV1` records. One listener matches current `Config`;
additional listeners are unsupported. Discovery is bounded by the native owner
and current runtime allowlists; enabling discovery grants neither trust nor
control and never creates bindings automatically. Unknown peers remain candidates.
A driver must validate the listener against available interfaces and exposure
policy before applying it. The pure validator checks shape, not machine availability.

The Portal presents enable/listener/discovery settings, the existing native
trust/discovery inspection, compatible promoted capability choices, and then
participant/capability/direction/permission for each selected binding. The
operator reviews the exact configuration delta before applying sensitive changes.
`send` means local capability to the selected peer capability; `receive` means
the reverse. Both sides use stable references; the local participant is in the
configuration. `observe` and `control` are separately selected permissions;
control can never be inferred from observation, a functional mapping or peer trust.

References (`LocalParticipant`, peer, capabilities, selection and binding ID)
are owner-allocated opaque 128-bit tokens encoded as 32 lowercase hex characters.
They must not encode or derive from a device fingerprint, SKI, serial, address,
discovery order or display name. The owner durably resolves them to native
identity/capability and the established semantic selection, and reserves deleted
IDs against reuse. Portal handles from `AdminV1` are short-lived authority handles,
not persistent references: reacquire them from a fresh native admin snapshot.
Exact selected identity and capability compatibility must be rechecked on use.
Persisting a token without the owner's durable resolution is an incomplete setup,
which stays configured/error and cannot be silently repaired by choosing a peer.

`ParticipationDriverV1.Participation` supplies the saved configuration, the
actually applied revision, a capture time, listener state and one observation
for every saved binding, including unavailable ones. `OperatorAdmin` supplies
existing `AdminV1` for bounded discovered/candidate/trusted/connected views,
selection, connect/confirm, cancel, retry and untrust. Trust action preconditions,
PIN privacy, observation expiry and native identity checks remain those of
`AdminV1`; Portal adds no parallel trust database or HA session contract.
HTTP/Ingress authentication does not establish native eeBUS trust.

`ApplyParticipation` takes the complete candidate configuration and existing
`MutationPreconditionV1` (native expected state revision and idempotency key).
Gateway checks its saved-revision conflict first; the driver checks native state
preconditions and the immutable binding targets. All new driver calls must have
a context deadline of at most 30 seconds; timed-out application returns an
explicit recovery/error result and requires fresh inspection before retry.
Idempotent retries reuse the key and identical payload, never launch competing
apply operations. Error text is a fixed safe category, never a native payload.
`ParticipationApplyResultV1` identifies native state revision, applied revision,
state and failure; returning from a call is not evidence that a device operated.
Gateway saves atomically only after validation, retains the previous valid
configuration on save failure, and displays a saved but unapplied revision after
apply failure. Native persistence and generic configuration persistence are not
assumed to be one transaction: interrupted application is reconciled explicitly.

### Trust, Mapping, Permission And Observed State

| State | Meaning and required display |
| --- | --- |
| `configured` | Desired binding is saved; operation has not been established (including disabled or restart-pending configuration). |
| `offline` | The bound participant is unavailable; saved identity, capability, permission and last evidence remain visible with age. |
| `incompatible` | Native capability/version/direction compatibility is negatively established; control is unavailable. |
| `active` | Current saved revision is applied, listener/peer are available, peer is trusted, capabilities are compatible, selected state is promoted and native negotiation is established. |
| `error` | Listener, persistence, authorization, negotiation or recovery failed; the safe failure category and control outcome are visible. |

Compatibility is explicitly `unknown`, `compatible` or `incompatible`; admission
is `unknown`, `candidate`, `qualified`, `promoted` or `unsupported`. The driver
reports the producer's current admission for the configured selection, rather
than elevating a candidate because it was selected in the UI. False/unknown trust,
availability or negotiation never establishes active operation. Inspection uses
current capture time plus retained native evidence; a partial read failure must
preserve each valid field and mark affected observations unavailable/stale.
A binding disappears only through explicit configuration removal.

`Granted` is the native negotiated permission subset of the saved permissions;
active operation requires at least one such grant.
`ValidateParticipationControlV1` is a pure eligibility check, not authorization
or dispatch. The actual native control owner must also require authenticated
operator authority, action-time confirmation for the exact target/direction/
operation, current promotion and compatibility, a fresh non-expired observation,
and its existing mutation/read-token/connection-generation preconditions. A
stale snapshot or a saved control permission alone never suffices. Incoming
peer control is subject to the same explicit target policy. No new generic
control endpoint or executable command is defined here. A retained control outcome is historical, not a current grant. Accepted control
means peer acceptance only, not verified effect; rejected or uncertain control is an
error with `rejected` or `outcome_unknown`, never successful operation.

`ParticipationProvenanceV1` retains the existing redacted evidence envelope (capture/data timestamps,
object digests and validated content hash) and
observed SHIP transport/SPINE data-model versions (null means unknown).
A null envelope represents unavailable evidence; active state requires a hashed
envelope. Bindings and evidence/loss collections are bounded at 256 records. Native
snapshots and the existing protected admin views retain full native detail under
their own authorization/redaction boundary. A non-null `Losses` array is the
producer's explicit semantic projection assessment: empty means assessed with no
reported loss, while null means unassessed. Each loss has a native observation
path and `omitted`, `transformed` or `unknown` code. Unassessed/unknown loss blocks
active state; any known loss whose `ControlSafe` qualification is false blocks
control eligibility. `ControlSafe` must come from the owning qualification,
never a Portal checkbox. Version labels, references and fixture values are not
normative specification or device compatibility evidence.

### Disable, Removal, Restart And Recovery

Disabling participation stops discovery/listening and new negotiations/control,
cancels outstanding work, and closes native sessions within the apply deadline.
It retains protected local identity, trusted peers, binding IDs and configuration
preferences. The existing disabled runtime accepts only empty `Config`: an
adapter applies that shape, keeping the saved participation settings separately;
re-enabling constructs a fresh runtime with the retained native state root.
Disable failure is an explicit error; it never reports the listener stopped
without observation. Disabling discovery alone does not untrust existing peers.

Removing a binding revokes its functional permission and cancels its in-flight
work before reporting removal applied; it does not untrust its peer or reset
local identity. Untrust is the separate confirmed native `AdminV1` action: it
invalidates grants and suspends all affected bindings without deleting their
saved records. Replacing a target, direction or selection requires a new binding
ID and an explicit reviewed delta. An identity reset is a separate authorized
operation, outside ordinary participation configuration; it cannot happen as a
side effect of disabling or deleting the last binding.

Restart/reconnect reloads the versioned saved bindings and the owner's durable
reference/trust resolution before negotiation. It preserves target identity and
selection, but recomputes availability, admission, compatibility and grants from
fresh evidence. No prior `active` flag or permission grant is restored as
operating authority. An unavailable/changed peer remains offline/incompatible;
neither name matching nor discovery can reassign it. Failed apply/recovery keeps
last-known-good fields and native identity; use at most one in-flight apply and
three context-bounded attempts per operator request, then surface an error and
require explicit retry. No background recovery may restore control after a
revocation, removal or disable. The validators enforce shape, state prerequisites
and immutable targets; fixtures demonstrate persistence round trips and negative
states only. Real atomic storage, timeout/cancellation, tombstones, cross-owner
reconciliation and physical effects remain implementation/integration gates.

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
