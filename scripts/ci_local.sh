#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export GOWORK=off

echo "==> terminology gate"
if git grep -nIwiE 'm[a]ster|s[l]ave' -- ':!vendor/'; then
  echo "Found legacy terminology."
  exit 1
fi

./scripts/api_boundary_gate.sh

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
