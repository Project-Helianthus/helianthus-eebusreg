package eebusraw

import (
	"fmt"
	"strings"
	"testing"
)

func TestMutationRequestDiagnosticFormattingRedactsSecrets(t *testing.T) {
	const (
		readToken      = "read-token-canary-85"
		idempotencyKey = "idempotency-canary-85"
	)
	values := []any{
		FeatureDataSetRequestV1{
			ReadToken:      readToken,
			IdempotencyKey: idempotencyKey,
		},
		MutationRollbackRequestV1{
			MutationRef:    "mutation-reference",
			IdempotencyKey: idempotencyKey,
		},
	}
	for _, value := range values {
		for _, formatted := range []string{
			fmt.Sprintf("%s", value),
			fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value),
		} {
			if strings.Contains(formatted, readToken) ||
				strings.Contains(formatted, idempotencyKey) {
				t.Fatalf("secret-bearing request leaked through diagnostic formatting: %q", formatted)
			}
			if !strings.Contains(formatted, "[redacted]") {
				t.Fatalf("diagnostic formatting omitted redaction marker: %q", formatted)
			}
		}
	}
}
