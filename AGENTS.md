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

## Production Go comments

The [API boundary checker](internal/apiboundary/main.go), in
`inspectGoBoundary`, checks comments in all non-test `.go` files outside `.git`,
`vendor` and `docs` directories (case-insensitive for `docs`), skipping symlinks.
`checkProductionComments` rejects every comment group except these three cases:

- The file's package doc comment: one `// Package <name> …` line with non-empty
  prose and at most 120 characters, including the `// ` prefix. Invalid package
  comments fail with `package comment must be one concise line starting with Package <name>`.
- Exactly one canonical `//go:build` line, in its own comment group before the
  package clause and separated from it by at least one blank line, as checked by
  `allowedCompilerCommentGroups` and `isCanonicalBuildConstraintGroup`.
- A machine-only cgo preamble attached to an unaliased `import "C"`, accepted by
  `allowedCompilerCommentGroups` and `isMachineOnlyCGOPreamble`.

Doc comments on exported identifiers, license headers, `//go:generate`,
`//go:embed` and other `//go:` lines are not exempt. Rejected non-package comment
groups report `<path>:<line>: production Go comment is not allowed`.

Put repository-local guidance in root `README.md` or `AGENTS.md`, and protocol
and API prose in the separate `helianthus-docs-eebus` repository.
`validateRepositoryPaths` and `isDocumentationPath` reject every other
Markdown/documentation path; any `docs` path segment (case-insensitive) is also
rejected by the same boundary gate.

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
