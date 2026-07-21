package main_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue54ProductionHasNoCompatibilityPublisherOrOutboundFabricator(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	banned := [][]byte{
		[]byte("Compat" + "MDNS"),
		[]byte("startLAN" + "SHIPPublisher"),
		[]byte("lan" + "SHIPPublisher"),
		[]byte("newLAN" + "SHIPMDNSProvider"),
		[]byte("QueueRemote" + "SKI("),
		[]byte("ReportRemote" + "Endpoint("),
	}
	var findings []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, token := range banned {
			if bytes.Contains(payload, token) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				findings = append(findings, relative+":"+string(token))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("obsolete compatibility/outbound publisher paths remain: %v", findings)
	}
}

func TestIssue54RepositoryHasNoPythonPublisher(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var findings []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.HasSuffix(name, ".py") && strings.Contains(name, "publish") {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			findings = append(findings, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("Python compatibility publishers remain: %v", findings)
	}
}
