#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
export GOWORK=off

echo "==> public package boundary"
bad_packages="$(go list -f '{{.Name}} {{.ImportPath}}' ./... | awk '$1 !~ /^(eebusruntime|eebusraw|eebusevidence)$/ {print}')"
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

echo "==> public export boundary"
if git grep -nE '^(type|func|const|var) +(.*Registry|.*Projection|.*Semantic|.*Enbility|.*Ship|.*SHIP|.*Spine|.*SPINE)' -- '*.go' ':!internal/**'; then
  echo "Public API exposes a forbidden boundary term."
  exit 1
fi

echo "==> no premature runtime surfaces"
if git grep -nE 'net\\.Listen|tls\\.Listen|ListenAndServe|/data/eebus|TrustStore|trust_store|truststore' -- '*.go'; then
  echo "Bootstrap must not contain listener or trust-store code."
  exit 1
fi
