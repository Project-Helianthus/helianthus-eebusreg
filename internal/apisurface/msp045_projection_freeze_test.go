package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const msp045FrozenPublicAPIHash = "2cc4ebe0e21704e879f882d878e549ed5dcbc3ec3e12240ccc804e3a4b381081"

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
	if len(payload) != 107_815 {
		t.Fatalf("public API bytes = %d, want 107815", len(payload))
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
