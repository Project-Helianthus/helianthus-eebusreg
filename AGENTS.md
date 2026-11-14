# AGENTS.md

## Purpose and ownership

`helianthus-eebusreg` owns raw eeBUS runtime integration, protocol-native
identity and qualification, evidence references, and bounded native snapshots.
It also owns the eeBUS participation and device-binding contract consumed by
Gateway. Universal semantics, consumer interpretation, Gateway policy and
bindings for other output protocols remain with their respective owners.

Public packages are limited to `eebusruntime`, `eebusraw`, and `eebusevidence`.
Keep SHIP/SPINE and vendor-specific types behind repository-local boundaries;
do not export them into protocol-neutral layers. Preserve raw evidence and make
candidate, qualified, promoted, unsupported, and unknown states explicit.

## Workflow

1. Reconcile the target branch, local changes, linked issue, and open pull
   requests before editing. Work one scoped issue at a time.
2. Create `issue/<number>-<slug>` from the current integration branch and keep
   the change within the issue acceptance criteria.
3. Use RED-first tests when behavior, protocol, persistence, recovery, or
   concurrency changes; keep partial failures field-preserving where the owning
   contract permits it.
4. Run `./scripts/ci_local.sh` and `git diff --check` before pushing.
5. Open a linked pull request with commands, results, scope, documentation-gate
   classification, and residual risk.
6. Obtain a fresh exact-HEAD blocker review and resolve P0-P2 findings before
   merge. Squash merge only with green applicable checks and review, then verify
   the remote integration branch. Never merge without requested authorization.

## Go comments and documentation ownership

Comments explain the code; exported APIs have godoc. Review comments for useful
information beyond the identifier name and reject boilerplate. Protocol semantics
link to the canonical `helianthus-docs-eebus` repository. Never reproduce
restricted-source material. Ordinary package and declaration documentation,
license headers, and detached explanatory comments are permitted. The API boundary
checker validates build constraints without imposing a prose-comment policy.

The documentation boundary follows the Board's 2026-07-10 instruction; its work is
tracked in the release Project and the owning issue of this repository:

> „auzi, dar helianthus-eebusreg/docs nu ar trebui să fie în helianthus-docs-eebus?”
>
> „Chiar mi-as dori ca toata documentatia sa fie in afara codebase-ului, pe modelul helianthus-docs-ebus. Documentatia de protocol diferita de documentatia de arhitectura, diferita de documentatia de API in docs-eebus. In nici un alt repo nu am avut un docs/ si nici nu ar trebui sa avem.”

Current work is tracked in the owning repository issues and release projects;
canonical documentation belongs in `helianthus-docs-eebus`.

Put repository-local guidance in root `README.md` or `AGENTS.md`. Protocol,
architecture, API, harness, and user documentation belongs in the separate
`helianthus-docs-eebus` repository. `validateRepositoryPaths` and
`isDocumentationPath` reject other Markdown/documentation paths; any `docs` path
segment (case-insensitive) is also rejected. Removing the comment prohibition does
not permit moving native documentation into this repository.

Run `./scripts/ci_local.sh`, which calls `scripts/api_boundary_gate.sh`, or run
`./scripts/api_boundary_gate.sh` directly to verify enforcement.

## Safety, evidence, and privacy

- Reads and discovery must be allowlisted, bounded, version-aware, and
  fail-closed. Writes and live-device actions need an explicit contract and
  action-time operator authorization.
- Never wholesale replace last-known-good state after a partial read failure.
- Do not commit credentials, private keys, trust-store bytes, private captures,
  private network information, serials, account data, or device fingerprints.
- Base durable claims on publishable evidence; label inference and unknowns.
  Do not reproduce restricted-source material.

## Public references

- [Helianthus eeBUS registry](https://github.com/Project-Helianthus/helianthus-eebusreg)
- [Canonical eeBUS documentation](https://github.com/Project-Helianthus/helianthus-docs-eebus)
- [EEBUS Initiative](https://www.eebus.org/)
