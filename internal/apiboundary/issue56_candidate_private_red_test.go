package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue56CandidateReferencesStayOutOfPublicAndSerializedContracts(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, directory := range []string{".", "eebusraw", "eebusevidence"} {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			issue56RejectCandidateTokens(t, filepath.Join(root, directory, entry.Name()))
		}
	}

	storeRoot := filepath.Join(root, "internal", "eebusstore")
	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		issue56RejectCandidateTokens(t, filepath.Join(storeRoot, entry.Name()))
	}
}

func issue56RejectCandidateTokens(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ToLower(string(data))
	for _, forbidden := range []string{"candidate_ref", "candidateref", "pairingcandidate"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("%s leaks private candidate token %q", path, forbidden)
		}
	}
}
