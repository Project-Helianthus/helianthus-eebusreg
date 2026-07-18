package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const msp045FrozenPublicAPIHash = "c93492bd275b5e14d3c9e05da701730d6d34a197e0653e6b169d103418bfcc8c"

func TestMSP045PublicAPIRemainsByteIdenticalAndProjectionFree(t *testing.T) {
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
	if len(payload) != 95_207 {
		t.Fatalf("public API bytes = %d, want 95207", len(payload))
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != msp045FrozenPublicAPIHash {
		t.Fatalf("public API SHA-256 = %s, want %s", got, msp045FrozenPublicAPIHash)
	}

	forbidden := []string{
		"Trust" + "Admin" + "Projection",
		"Candidate" + "Finger" + "print",
		"Admin" + "Path",
		"Pairing" + "History",
	}
	for _, pkg := range doc.Packages {
		for _, symbol := range pkg.Symbols {
			declaration := symbol.Name + " " + symbol.Signature + " " + symbol.Type
			for _, name := range forbidden {
				if strings.Contains(declaration, name) {
					t.Fatalf("public declaration %s.%s exposes internal projection data", pkg.Path, symbol.Name)
				}
			}
		}
	}
}
