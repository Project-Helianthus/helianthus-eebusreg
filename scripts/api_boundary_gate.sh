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
bad_packages="$(go list -f '{{.Name}} {{.ImportPath}}' ./... | awk '$2 !~ /\/internal(\/|$)/ && $1 !~ /^(eebusruntime|eebusraw|eebusevidence)$/ {print}')"
if [ -n "$bad_packages" ]; then
  echo "Unexpected public package names:"
  echo "$bad_packages"
  exit 1
fi

echo "==> enbility import boundary"
if git_grep_checked -n 'github.com/enbility' -- '*.go' ':!internal/**'; then
  echo "Direct enbility imports are allowed only under internal/."
  exit 1
fi

echo "==> public API AST boundary"
go run ./internal/apiboundary

echo "==> no premature runtime surfaces"
if git_grep_checked -nE 'net\\.Listen|tls\\.Listen|ListenAndServe|/data/eebus|TrustStore|trust_store|truststore' -- '*.go' ':!internal/apiboundary/**' ':!*_test.go'; then
  echo "Implementation must not contain listener or trust-store code in this milestone."
  exit 1
fi

echo "==> no premature trust or pairing mutation API"
if git_grep_checked -nE '^(type|func|const|var) +(RegisterRemoteSKI|UnregisterRemoteSKI|SetPairingWindow|.*PairingWindow|.*TrustStore|.*TrustMutation)' -- '*.go' ':!internal/**'; then
  echo "Public API exposes premature trust or pairing mutation surface."
  exit 1
fi
