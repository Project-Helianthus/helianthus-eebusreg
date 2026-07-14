//go:build darwin

package eebusstore

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	original, wasSet := os.LookupEnv("TMPDIR")
	canonical, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil || !filepath.IsAbs(canonical) {
		fmt.Fprintln(os.Stderr, "eebusstore TestMain: canonicalize TMPDIR failed")
		os.Exit(2)
	}
	if err := os.Setenv("TMPDIR", canonical); err != nil {
		fmt.Fprintln(os.Stderr, "eebusstore TestMain: set TMPDIR failed")
		os.Exit(2)
	}

	code := m.Run()
	if wasSet {
		err = os.Setenv("TMPDIR", original)
	} else {
		err = os.Unsetenv("TMPDIR")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "eebusstore TestMain: restore TMPDIR failed")
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}
