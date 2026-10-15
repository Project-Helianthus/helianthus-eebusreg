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

Do not add ordinary explanatory comments to production Go files, including
internal packages. Keep explanatory prose in the repository's permitted Markdown
documentation. The API boundary checker applies this rule to non-test Go files
within its scan scope and reports `production Go comment is not allowed`.

Verify enforcement with `./scripts/api_boundary_gate.sh`, which runs
`go run ./internal/apiboundary`. It is also reached by `./scripts/ci_local.sh`
through the toolchain proof and API boundary steps; there is no separate comment
gate script. See [the checker](internal/apiboundary/main.go)
(`checkProductionComments`, `allowedCompilerCommentGroups`, and the scan in
`inspectGoBoundary`) and [its regression tests](internal/apiboundary/docs_clean_test.go).
The checker permits a constrained one-line package comment and narrowly
recognized build constraints and machine-only cgo preambles; consult those
functions for the exact conditions rather than assuming a general exemption.
Preserve existing license notices and compiler directives; this guidance does
not authorize removing them or expanding the checker's allowances. If such a
notice or directive conflicts with the checker, report the conflict for review.

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
