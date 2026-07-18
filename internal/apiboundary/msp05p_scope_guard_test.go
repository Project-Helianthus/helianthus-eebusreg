package main_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMSP05PCodeRepositoryHasNoDocsTree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("code repository gained a docs tree: %v", err)
	}
}
