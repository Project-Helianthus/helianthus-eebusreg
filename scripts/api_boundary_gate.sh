#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export GOWORK=off

echo "==> public package boundary"
bad_packages="$(go list -f '{{.Name}} {{.ImportPath}}' ./... | awk '$2 !~ /\/internal(\/|$)/ && $1 !~ /^(eebusruntime|eebusraw|eebusevidence)$/ {print}')"
if [ -n "$bad_packages" ]; then
  echo "Unexpected public package names:"
  echo "$bad_packages"
  exit 1
fi

echo "==> enbility import boundary"
if git grep -n 'github.com/enbility' -- '*.go' ':!internal/**'; then
  echo "Direct enbility imports are allowed only under internal/."
  exit 1
fi

echo "==> public API AST boundary"
go run ./internal/apiboundary

echo "==> no premature runtime surfaces"
if git grep -nE 'net\\.Listen|tls\\.Listen|ListenAndServe|/data/eebus|TrustStore|trust_store|truststore' -- '*.go' ':!internal/**'; then
  echo "Public API must not contain listener or trust-store code."
  exit 1
fi

echo "==> no premature trust or pairing mutation API"
if git grep -nE '^(type|func|const|var) +(RegisterRemoteSKI|UnregisterRemoteSKI|SetPairingWindow|.*PairingWindow|.*TrustStore|.*TrustMutation)' -- '*.go' ':!internal/**'; then
  echo "Public API exposes premature trust or pairing mutation surface."
  exit 1
fi
