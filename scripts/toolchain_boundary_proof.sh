#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

export GOWORK=off
export GOTOOLCHAIN=local

eebus_module_path="github.com/Project-Helianthus/helianthus-eebus-go"
eebus_module_version="v0.7.1-helianthus.4"
ship_module_path="github.com/Project-Helianthus/helianthus-ship-go"
ship_module_version="v0.6.1-helianthus.5"

echo "==> toolchain boundary proof"
go version
go env GOVERSION GOTOOLCHAIN GOWORK GOMOD GOMODCACHE GOPATH
active_go="$(go env GOVERSION)"
max_go="${HELIANTHUS_EEBUSREG_MAX_GO_VERSION:-$active_go}"

if [ "$(go env GOWORK)" != "off" ]; then
  echo "GOWORK must be off" >&2
  exit 1
fi
if [ "$(go env GOTOOLCHAIN)" != "local" ]; then
  echo "GOTOOLCHAIN must be local" >&2
  exit 1
fi

echo "==> preflight go.mod replace scan"
replace_matches="$(awk '
  {
    line = $0
    sub(/\/\/.*/, "", line)
    sub(/^[[:space:]]+/, "", line)
    if (line ~ /^replace([[:space:]\(]|$)/) {
      print FILENAME ":" NR ":" $0
    }
  }
' go.mod)"
if [ -n "$replace_matches" ]; then
  echo "replace directives are not allowed before module graph proof:" >&2
  echo "$replace_matches" >&2
  exit 1
fi

eebus_module_json="$(mktemp)"
ship_module_json="$(mktemp)"
module_graph="$(mktemp)"
cleanup() {
  rm -f "$eebus_module_json" "$ship_module_json" "$module_graph"
}
trap cleanup EXIT

echo "==> module pin"
go list -m -json "$eebus_module_path" | tee "$eebus_module_json"
go list -m -json "$ship_module_path" | tee "$ship_module_json"

echo "==> go.mod boundary"
go run -mod=readonly ./internal/toolchainproof \
  -repo-root "$repo_root" \
  -max-go "$max_go" \
  -active-go "$active_go" \
  -module "$eebus_module_path" \
  -version "$eebus_module_version" \
  -module-json "$eebus_module_json"
go run -mod=readonly ./internal/toolchainproof \
  -repo-root "$repo_root" \
  -max-go "$max_go" \
  -active-go "$active_go" \
  -module "$ship_module_path" \
  -version "$ship_module_version" \
  -module-json "$ship_module_json"

echo "==> public boundary"
./scripts/api_boundary_gate.sh

echo "==> tidy diff"
tidy_out="$(mktemp)"
tidy_err="$(mktemp)"
cleanup_tidy() {
  rm -f "$tidy_out" "$tidy_err"
}
trap 'cleanup; cleanup_tidy' EXIT
set +e
go mod tidy -diff >"$tidy_out" 2>"$tidy_err"
tidy_status=$?
set -e
if [ "$tidy_status" -eq 0 ]; then
  cat "$tidy_out"
elif grep -q "flag provided but not defined: -diff" "$tidy_err"; then
  go mod tidy
  git diff --exit-code -- go.mod go.sum
else
  cat "$tidy_out"
  cat "$tidy_err" >&2
  exit "$tidy_status"
fi
cleanup_tidy
trap cleanup EXIT

echo "==> module verification"
go mod verify

echo "==> module graph"
go list -m all | tee "$module_graph"

echo "==> module graph checksums"
if command -v shasum >/dev/null 2>&1; then
  shasum -a 256 go.mod go.sum "$module_graph"
elif command -v sha256sum >/dev/null 2>&1; then
  sha256sum go.mod go.sum "$module_graph"
else
  echo "no sha256 checksum tool found" >&2
  exit 1
fi

echo "==> tests"
go test ./...

echo "==> build"
go build ./...

echo "toolchain boundary proof passed"
