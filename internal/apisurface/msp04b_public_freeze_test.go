package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const msp04bFrozenPublicAPIHash = "c93492bd275b5e14d3c9e05da701730d6d34a197e0653e6b169d103418bfcc8c"

func TestMSP04BPublicAPIRemainsExactlyFrozen(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	doc = msp05pProjectFrozenV1(t, doc)
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != msp04bFrozenPublicAPIHash {
		t.Fatalf("public API hash = %s, want frozen MSP-04B baseline", got)
	}
}

func TestMSP04BG16RootSurfaceHasNoMutationOrCandidateDetail(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var root *surface
	for index := range doc.Packages {
		if doc.Packages[index].Path == modulePath {
			root = &doc.Packages[index]
			break
		}
	}
	if root == nil {
		t.Fatal("root public package missing")
	}
	forbidden := []string{
		"candidate",
		"candidate_nonce",
		"connection_generation",
		"expires_at",
		"idempotency",
		"starting_store_generation",
		"admin_path",
		"openpairing",
		"closepairing",
		"confirmpairing",
		"cancelpairing",
		"registerremoteski",
		"unregisterremoteski",
		"trustmutation",
	}
	for _, symbol := range root.Symbols {
		contract := strings.ToLower(symbol.Name + " " + symbol.Signature)
		for _, fragment := range forbidden {
			if strings.Contains(contract, fragment) {
				t.Fatalf("public symbol %q leaks forbidden MSP-04B fragment %q", symbol.Name, fragment)
			}
		}
	}
}
