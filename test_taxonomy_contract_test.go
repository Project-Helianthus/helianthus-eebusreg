package eebusruntime

import (
	"errors"
	"os"
	"testing"
)

var observableTestTaxonomyV1 = map[string]string{
	"issue77_review_contract_regression_test.go": "raw_snapshot_redaction_contract_test.go",
	"runtime_api_v1_cleanup_red_test.go":         "runtime_configuration_contract_test.go",
	"raw_feature_runtime_v1_red_test.go":         "raw_feature_read_runtime_test.go",
	"raw_mutation_outcome_v1_red_test.go":        "raw_mutation_outcome_clone_test.go",
	"raw_mutation_runtime_v1_red_test.go":        "raw_mutation_authorization_test.go",
}

func TestObservableTestTaxonomyReplacesHistoricalRootFilenames(t *testing.T) {
	for historical, observable := range observableTestTaxonomyV1 {
		if _, err := os.Stat(historical); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("historical test filename %q remains after taxonomy migration: %v", historical, err)
		}
		if info, err := os.Stat(observable); err != nil || info.IsDir() {
			t.Fatalf("observable test filename %q is unavailable: %v", observable, err)
		}
	}
}
