# AGENTS.md

## Purpose and ownership

`helianthus-eebusreg` owns raw eeBUS runtime integration, protocol-native
identity and qualification, evidence references, and bounded native snapshots.
It is not the owner of universal semantics, consumer interpretation, gateway
policy, or output bindings.

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
