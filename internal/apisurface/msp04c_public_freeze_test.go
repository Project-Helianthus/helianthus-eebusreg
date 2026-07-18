package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestMSP04CG16PublicGoAPIRemainsAtTheMSP04BFrozenHash(t *testing.T) {
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
		t.Fatalf("public API hash = %s, want the frozen MSP-04B baseline", got)
	}
}

func TestMSP04CG16PublicPackagesExposeNoRecoveryMutationSurface(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"adoptcopied",
		"anchor",
		"backoff",
		"controlhighwater",
		"idempotency",
		"manifestrollback",
		"quarantine",
		"reconcilepublication",
		"recoveridentity",
		"repair",
		"retryattempt",
		"revokeassociation",
		"tombstone",
	}
	for _, pkg := range doc.Packages {
		for _, symbol := range pkg.Symbols {
			contract := strings.ToLower(symbol.Name + " " + symbol.Signature)
			for _, fragment := range forbidden {
				if strings.Contains(contract, fragment) {
					t.Fatalf("public symbol %s.%s exposes forbidden recovery fragment %q", pkg.Path, symbol.Name, fragment)
				}
			}
		}
	}
}
