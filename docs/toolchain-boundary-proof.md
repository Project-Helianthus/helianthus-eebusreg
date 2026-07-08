# Toolchain Boundary Proof

Status: `MSP-03B`

Issue: Project-Helianthus/helianthus-eebusreg#10

## Scope

MSP-03B proves that the raw eeBUS module builds with explicit module and
toolchain boundaries after the internal `eebus-go v0.7.0` facade spike.

The proof is still pre-runtime. It does not start SHIP/SPINE services, open
listeners, create credentials, persist trust state, import the gateway, expose
MCP tools, or run HA networking checks.

## Required Environment

The proof forces:

```bash
GOWORK=off
GOTOOLCHAIN=local
```

The configured maximum Go language version defaults to the active Go binary.
CI pins this explicitly: the normal validation lane uses `1.22`, and the
build-container lane uses `1.26` to match the current HA add-on builder image.
`go.mod`, any future `toolchain` directive, and the active Go binary must not
exceed the configured maximum language version for that lane.

## Checks

`scripts/toolchain_boundary_proof.sh` verifies:

- `go env` reports `GOWORK=off` and `GOTOOLCHAIN=local`;
- `go.mod` contains no `replace` directive before any module-graph-dependent
  checker is run;
- `github.com/enbility/eebus-go` resolves to exactly `v0.7.0`;
- no `replace` directives are present;
- no active Go binary, `go`, or `toolchain` directive exceeds the configured
  maximum language version;
- direct `github.com/enbility/...` imports remain under `internal/`;
- public package and exported API boundary gates still pass;
- tidy diff semantics pass (`go mod tidy -diff` when supported, otherwise
  `go mod tidy` followed by a clean `git diff --exit-code -- go.mod go.sum`);
- `go mod verify`, `go list -m all`, `go test ./...`, and `go build ./...`
  pass;
- `go.mod`, `go.sum`, and module graph checksums are emitted for review.

GitHub CI runs the same script in the normal Go 1.22 validation lane and in a
Docker `golang:1.26-alpine` build-container lane matching the current HA add-on
builder image. The Docker lane configures `/workspace` as a Git safe directory
before running grep-backed gates. Local Docker is useful but not required for
developer execution of the local proof.
