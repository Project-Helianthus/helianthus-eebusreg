package eebusmutation

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue87MutationReferenceMatchesCanonicalOpaqueReference(t *testing.T) {
	reference := rawMutationReference(
		[]byte("0123456789abcdef0123456789abcdef"),
		eebusraw.HashV1("sha256:"+strings.Repeat("a", 64)),
	)
	decoded, err := base64.RawURLEncoding.DecodeString(reference)
	if err != nil || len(reference) != 43 || len(decoded) != 32 {
		t.Fatalf("mutation reference = %q, decoded=%d, error=%v", reference, len(decoded), err)
	}
}
