#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export GOWORK=off

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

echo "==> no replace directives"
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
  echo "replace directives are not allowed:" >&2
  echo "$replace_matches" >&2
  exit 1
fi

echo "==> public package boundary"
module_path="$(go list -m)"
bad_packages="$(go list -f '{{.Name}} {{.ImportPath}}' ./... | awk -v driver="$module_path/driver" '$2 !~ /\/internal(\/|$)/ && $1 !~ /^(eebusruntime|eebusraw|eebusevidence)$/ && $2 != driver && index($2, driver "/") != 1 {print}')"
if [ -n "$bad_packages" ]; then
  echo "Unexpected public package names:"
  echo "$bad_packages"
  exit 1
fi

echo "==> enbility import boundary"
enbility_imports="$(git grep -n 'github.com/enbility' -- '*.go' ':!internal/**' || [ $? -eq 1 ])"
enbility_rejected="$(printf '%s\n' "$enbility_imports" | grep -Ev '^driver/.*:[0-9]+:[[:space:]]*((import|_|\.|[A-Za-z][A-Za-z0-9_]*)[[:space:]]+)?"github\.com/enbility/(eebus-go|ship-go|spine-go)(/[^"]*)?"[[:space:]]*$' || [ $? -eq 1 ])"
if [ -n "$enbility_rejected" ]; then
  printf '%s\n' "$enbility_rejected"
  echo "Direct enbility imports are allowed only under internal/, and the pinned eeBUS stack under driver/."
  exit 1
fi

echo "==> public API AST boundary"
go run ./internal/apiboundary "$@"

echo "==> no premature runtime surfaces"
if git_grep_checked -nE 'net\\.Listen|tls\\.Listen|ListenAndServe|/data/eebus|TrustStore|trust_store|truststore' -- '*.go' ':!internal/apiboundary/**' ':!internal/eebusinteropsmoke/**' ':!*_test.go'; then
  echo "Implementation must not contain listener or trust-store code in this milestone."
  exit 1
fi

echo "==> no premature trust or pairing mutation API"
if git_grep_checked -nE '^(type|func|const|var) +(RegisterRemoteSKI|UnregisterRemoteSKI|SetPairingWindow|.*TrustStore|.*TrustMutation)' -- '*.go' ':!internal/**'; then
  echo "Public API exposes premature trust or pairing mutation surface."
  exit 1
fi
