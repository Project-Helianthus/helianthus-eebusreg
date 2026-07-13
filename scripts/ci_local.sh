#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export GOWORK=off
export GOTOOLCHAIN=local

git_grep_checked() {
  set +e
  git grep "$@"
  status=$?
  set -e
  case "$status" in
    0) return 0 ;;
    1) return 1 ;;
    *)
      echo "git grep failed with status $status" >&2
      exit "$status"
      ;;
  esac
}

echo "==> terminology gate"
if git_grep_checked -nIwiE 'm[a]ster|s[l]ave' -- ':!vendor/'; then
  echo "Found legacy terminology."
  exit 1
fi

echo "==> toolchain boundary proof"
./scripts/toolchain_boundary_proof.sh

./scripts/api_boundary_gate.sh

echo "==> API boundary manifest artifact"
manifest_dir="$(mktemp -d)"
cleanup_manifest() {
  rm -rf "$manifest_dir"
}
trap cleanup_manifest EXIT
manifest_path="$manifest_dir/api-boundary.json"
./scripts/api_boundary_gate.sh -manifest "$manifest_path"
if [ ! -s "$manifest_path" ]; then
  echo "API boundary manifest artifact was not generated."
  exit 1
fi
cleanup_manifest
trap - EXIT

echo "==> internal package allowance smoke"
mkdir -p internal
tmp_internal="$(mktemp -d internal/boundary-smoke-XXXXXX)"
cleanup() {
  rm -rf "$tmp_internal"
}
trap cleanup EXIT
cat > "$tmp_internal/doc.go" <<'GO'
package facade
GO
./scripts/api_boundary_gate.sh
cleanup
trap - EXIT

echo "==> public API boundary negative smoke"
tmp_bad="boundary_negative_smoke.go"
cat > "$tmp_bad" <<'GO'
package eebusruntime

type (
	SpineProjection struct{}
)

func AcceptPairing() {}
GO
if ./scripts/api_boundary_gate.sh >/tmp/eebusreg-boundary-negative.out 2>&1; then
  cat /tmp/eebusreg-boundary-negative.out
  rm -f "$tmp_bad" /tmp/eebusreg-boundary-negative.out
  echo "Boundary gate accepted forbidden public API smoke fixture."
  exit 1
fi
rm -f "$tmp_bad" /tmp/eebusreg-boundary-negative.out

echo "==> gofmt"
unformatted="$(git ls-files '*.go' | xargs -n 50 gofmt -l || true)"
if [ -n "$unformatted" ]; then
  echo "gofmt required for:"
  echo "$unformatted"
  exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go build"
go build ./...

echo "==> go test (race)"
go test -race -count=1 ./...
