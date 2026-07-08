# AGENTS

This repository is part of the Helianthus Multi-Protocol HVAC Gateway
Platform.

## Repository Boundary

`helianthus-eebusreg` is raw eeBUS runtime and evidence plumbing. It is not a
semantic registry fork and does not own consumer-facing meaning.

Public Go packages are limited to:

- `eebusruntime`
- `eebusraw`
- `eebusevidence`

Direct `github.com/enbility/*` imports are allowed only under `internal/...`
and only after the M3 facade spike. Gateway imports are forbidden until the
M3.5 raw contract freeze and the later gateway sidecar issue.

## Workflow

1. Work one issue at a time.
2. Keep at most one open PR in this repository.
3. Run `./scripts/ci_local.sh` before pushing.
4. Link the canonical doc-gate result from
   `helianthus-docs-ebus/docs/platform`.
5. Keep raw eeBUS facts raw. Candidate or promoted semantic facts belong to
   later gated milestones.
