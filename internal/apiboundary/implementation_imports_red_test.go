package main_test

import "testing"

func TestAPIBoundaryRejectsCanonicalAndUpstreamImplementationImports(t *testing.T) {
	implementations := []struct {
		identity string
		module   string
	}{
		{identity: "canonical eebus-go", module: "github.com/Project-Helianthus/helianthus-eebus-go"},
		{identity: "canonical ship-go", module: "github.com/Project-Helianthus/helianthus-ship-go"},
		{identity: "canonical spine-go", module: "github.com/Project-Helianthus/helianthus-spine-go"},
		{identity: "upstream eebus-go", module: "github.com/enbility/eebus-go"},
		{identity: "upstream ship-go", module: "github.com/enbility/ship-go"},
		{identity: "upstream spine-go", module: "github.com/enbility/spine-go"},
	}
	tool := buildAPIBoundary(t)

	for _, implementation := range implementations {
		t.Run(implementation.identity, func(t *testing.T) {
			root := newSyntheticRepository(t)
			writeFile(t, root, "eebusraw/forbidden.go", "package eebusraw\n\nimport _ \""+implementation.module+"/api\"\n")
			expectRejected(t, tool, root, "direct protocol implementation", "internal")
		})
	}
}
