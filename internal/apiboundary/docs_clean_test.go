package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const minimalREADME = "# eeBUS Registry\n\nCanonical docs: Project-Helianthus/helianthus-docs-eebus.\n\nBuild: `./scripts/ci_local.sh`.\n"

func TestDocsCleanOwnershipContract(t *testing.T) {
	tool := buildAPIBoundary(t)

	t.Run("exact minimal README and concise package metadata are accepted", func(t *testing.T) {
		root := newSyntheticRepository(t)
		output, err := runTool(t, tool, root)
		if err != nil {
			t.Fatalf("exact external-only ownership fixture was rejected: %v\n%s", err, output)
		}
	})

	t.Run("permitted eebusraw API is accepted", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeFile(t, root, "eebusraw/api.go", `package eebusraw

type Envelope struct{ ID string }

func Observe(id string) Envelope { return Envelope{ID: id} }

const StateObserved = "observed"

const ToolID = "eebus.v1.snapshot.capture"
`)
		output, err := runTool(t, tool, root)
		if err != nil {
			t.Fatalf("permitted eebusraw API fixture was rejected: %v\n%s", err, output)
		}
	})

	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   []string
	}{
		{
			name: "tracked docs tree",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "docs/legacy.md", "synthetic legacy documentation\n")
				runGit(t, root, nil, "add", "--", "docs/legacy.md")
			},
			want: []string{"tracked", "docs/legacy.md"},
		},
		{
			name: "untracked docs tree",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "docs/draft.md", "synthetic draft documentation\n")
			},
			want: []string{"untracked", "docs/draft.md"},
		},
		{
			name: "ignored docs tree",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, ".gitignore", "docs/**\n")
				runGit(t, root, nil, "add", "--", ".gitignore")
				writeFile(t, root, "docs/ignored.md", "synthetic ignored documentation\n")
				runGit(t, root, nil, "check-ignore", "--", "docs/ignored.md")
			},
			want: []string{"docs/ignored.md"},
		},
		{
			name: "ignored top-level Markdown",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, ".gitignore", "/*.md\n!/README.md\n")
				runGit(t, root, nil, "add", "--", ".gitignore")
				writeFile(t, root, "ROADMAP.md", "synthetic ignored roadmap\n")
				runGit(t, root, nil, "check-ignore", "--", "ROADMAP.md")
			},
			want: []string{"ROADMAP.md"},
		},
		{
			name: "alternate documentation extensions",
			mutate: func(t *testing.T, root string) {
				for _, path := range []string{
					"AUTHORITY.txt",
					"notes/PROTOCOL.rst",
					"reference/API.adoc",
					"reference/API.asciidoc",
					"notes/overview.mdown",
					"notes/overview.mkd",
				} {
					writeFile(t, root, path, "synthetic documentation\n")
				}
			},
			want: []string{"AUTHORITY.txt", "notes/PROTOCOL.rst", "reference/API.adoc", "reference/API.asciidoc", "notes/overview.mdown", "notes/overview.mkd"},
		},
		{
			name: "documentation name without extension",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "notes/ARCHITECTURE", "synthetic architecture documentation\n")
			},
			want: []string{"notes/ARCHITECTURE"},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, root string) {
				replaceWithSymlink(t, root, "README.md", "AGENTS.md")
			},
			want: []string{"symlink", "README.md"},
		},
		{
			name: "traversal symlink target",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Dir(root), "outside-readme.txt", minimalREADME)
				replaceWithSymlink(t, root, "README.md", "../outside-readme.txt")
			},
			want: []string{"traversal", "README.md"},
		},
		{
			name: "absolute symlink target",
			mutate: func(t *testing.T, root string) {
				target := filepath.Join(filepath.Dir(root), "absolute-readme.txt")
				if err := os.WriteFile(target, []byte(minimalREADME), 0o644); err != nil {
					t.Fatal(err)
				}
				replaceWithSymlink(t, root, "README.md", target)
			},
			want: []string{"absolute", "README.md"},
		},
		{
			name: "relative eebusraw package metadata symlink",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "eebusraw/doc-target.txt", "// Package eebusraw defines raw eeBUS data contracts.\npackage eebusraw\n")
				replaceWithSymlink(t, root, "eebusraw/doc.go", "doc-target.txt")
			},
			want: []string{"symlink", "eebusraw/doc.go"},
		},
		{
			name: "traversal eebusraw package metadata symlink",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Dir(root), "outside-doc.go", "// Package eebusraw defines raw eeBUS data contracts.\npackage eebusraw\n")
				replaceWithSymlink(t, root, "eebusraw/doc.go", "../../outside-doc.go")
			},
			want: []string{"traversal", "eebusraw/doc.go"},
		},
		{
			name: "absolute eebusraw package metadata symlink",
			mutate: func(t *testing.T, root string) {
				target := filepath.Join(filepath.Dir(root), "absolute-doc.go")
				if err := os.WriteFile(target, []byte("// Package eebusraw defines raw eeBUS data contracts.\npackage eebusraw\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				replaceWithSymlink(t, root, "eebusraw/doc.go", target)
			},
			want: []string{"absolute", "eebusraw/doc.go"},
		},
		{
			name: "portable casefold collision",
			mutate: func(t *testing.T, root string) {
				blob := strings.TrimSpace(runGit(t, root, strings.NewReader(minimalREADME), "hash-object", "-w", "--stdin"))
				runGit(t, root, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+",ReadMe.md")
			},
			want: []string{"casefold", "ReadMe.md"},
		},
		{
			name: "extra Markdown outside allowlist",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "DESIGN.md", "synthetic design prose\n")
				runGit(t, root, nil, "add", "--", "DESIGN.md")
			},
			want: []string{"markdown", "DESIGN.md"},
		},
		{
			name: "substantive package comment",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "eebusraw/doc.go", `// Package eebusraw defines raw eeBUS data contracts.
//
// It also explains runtime policy, semantic promotion, and consumer behavior.
package eebusraw
`)
			},
			want: []string{"package comment", "eebusraw/doc.go"},
		},
		{
			name: "exported declaration comment",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "eebusraw/api.go", `package eebusraw

// Envelope carries a decoded frame and documents consumer policy.
type Envelope struct{}
`)
			},
			want: []string{"comment", "eebusraw/api.go"},
		},
		{
			name: "detached line prose comment",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "eebusraw/prose.go", `package eebusraw

// Runtime policy and consumer behavior belong in external documentation.
`)
			},
			want: []string{"comment", "eebusraw/prose.go"},
		},
		{
			name: "detached block prose comment",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "eebusraw/prose.go", `package eebusraw

/* Runtime policy and consumer behavior belong in external documentation. */
`)
			},
			want: []string{"comment", "eebusraw/prose.go"},
		},
		{
			name: "README drift",
			mutate: func(t *testing.T, root string) {
				writeFile(t, root, "README.md", minimalREADME+"Additional local documentation.\n")
			},
			want: []string{"README.md", "exact"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newSyntheticRepository(t)
			test.mutate(t, root)
			expectRejected(t, tool, root, test.want...)
		})
	}

	forbiddenExports := []struct {
		name     string
		exported string
		fragment string
	}{
		{name: "registry", exported: "RegistryEnvelope", fragment: "Registry"},
		{name: "semantic", exported: "SemanticZone", fragment: "Semantic"},
		{name: "graphql", exported: "GraphQLResolver", fragment: "GraphQL"},
		{name: "trust store", exported: "TrustStoreRecord", fragment: "TrustStore"},
	}
	for _, test := range forbiddenExports {
		t.Run("forbidden exported boundary/"+test.name, func(t *testing.T) {
			root := newSyntheticRepository(t)
			writeFile(t, root, "eebusraw/forbidden.go", "package eebusraw\n\ntype "+test.exported+" struct{}\n")
			expectRejected(t, tool, root, "forbidden boundary", test.fragment)
		})
	}

	t.Run("forbidden direct enbility type", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeFile(t, root, "eebusraw/forbidden.go", `package eebusraw

import shipapi "github.com/enbility/ship-go/api"

type ExternalConnection = shipapi.ShipConnectionDataReaderInterface
`)
		expectRejected(t, tool, root, "enbility", "internal")
	})

	t.Run("forbidden internal facade type", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeFile(t, root, "internal/facade/value.go", "package facade\n\ntype Value struct{}\n")
		writeFile(t, root, "eebusraw/forbidden.go", `package eebusraw

import "example.test/registry/internal/facade"

type FacadeValue = facade.Value
`)
		expectRejected(t, tool, root, "internal implementation")
	})

	t.Run("forbidden eBUS runtime identifier", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeFile(t, root, "eebusraw/forbidden.go", "package eebusraw\n\nconst ToolID = \"ebus.v1.snapshot.capture\"\n")
		expectRejected(t, tool, root, "eBUS runtime identifier")
	})

	unexpectedPackages := []string{"semantic", "registry", "helpers", "codec"}
	for _, packageName := range unexpectedPackages {
		t.Run("unexpected public package/"+packageName, func(t *testing.T) {
			root := newSyntheticRepository(t)
			writeFile(t, root, packageName+"/api.go", "package "+packageName+"\n")
			expectRejected(t, tool, root, "public package", packageName)
		})
	}
}

func TestStableContractASTManifest(t *testing.T) {
	tool := buildAPIBoundary(t)

	t.Run("canonical module rejects both stable roots removed", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeFile(t, root, "go.mod", "module github.com/Project-Helianthus/helianthus-eebusreg\n\ngo 1.22.0\n")
		writeFile(t, root, "eebusevidence/doc.go", "// Package eebusevidence defines raw eeBUS evidence contracts.\npackage eebusevidence\n")
		expectRejected(t, tool, root, "IdentityDocumentV1", "EnvelopeV1", "missing")
	})

	t.Run("stable closure accepts alpha declarations without adopting them", func(t *testing.T) {
		root := newSyntheticRepository(t)
		writeStableContractFixture(t, root, stableRawFixture(), stableEvidenceFixture())
		artifactPath := filepath.Join(t.TempDir(), "stable-contract.json")
		output, err := runTool(t, tool, root, "-manifest", artifactPath)
		if err != nil {
			t.Fatalf("exact stable contract fixture was rejected: %v\n%s", err, output)
		}
		var manifest apiManifest
		if err := json.Unmarshal(readArtifact(t, artifactPath), &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.StableContracts) != 2 {
			t.Fatalf("stable contract package count = %d, want 2", len(manifest.StableContracts))
		}
		stablePayload, err := json.Marshal(manifest.StableContracts)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"PairingObservation", "SessionState", "ToolID", "ScopePairingStatus", "eebus.v1."} {
			if bytes.Contains(stablePayload, []byte(forbidden)) {
				t.Fatalf("stable type closure adopted alpha/final authority %q: %s", forbidden, stablePayload)
			}
		}
		for _, required := range []string{"EndpointRoleV1", "EndpointIdentityV1", "SessionIdentityV1", "CaptureProvenanceV1", "RawSnapshotScopeV1", "capture_provenance"} {
			if !bytes.Contains(stablePayload, []byte(required)) {
				t.Fatalf("stable type closure omitted %q: %s", required, stablePayload)
			}
		}
	})

	t.Run("unexported typed stable enum helper is ignored", func(t *testing.T) {
		root := newSyntheticRepository(t)
		rawSource := strings.Replace(
			stableRawFixture(),
			"\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"\n)",
			"\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"\n\tendpointRoleV1Private EndpointRoleV1 = \"private\"\n)",
			1,
		)
		writeStableContractFixture(t, root, rawSource, stableEvidenceFixture())
		if output, err := runTool(t, tool, root); err != nil {
			t.Fatalf("private stable enum helper was rejected: %v\n%s", err, output)
		}
	})

	tests := []struct {
		name           string
		mutateRaw      func(string) string
		mutateEvidence func(string) string
		want           []string
	}{
		{
			name: "endpoint pairing field",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tUnknown []UnknownField `json:\"unknown,omitempty\"`\n}\n\ntype SessionIdentityV1", "\tPairing PairingObservation `json:\"pairing,omitempty\"`\n\tUnknown []UnknownField `json:\"unknown,omitempty\"`\n}\n\ntype SessionIdentityV1", 1)
			},
			want: []string{"EndpointIdentityV1", "exact field/type/tag manifest"},
		},
		{
			name: "session lifecycle state field",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tRemoteID RedactedID `json:\"remote_id\"`\n\tUnknown", "\tRemoteID RedactedID `json:\"remote_id\"`\n\tState SessionState `json:\"state\"`\n\tUnknown", 1)
			},
			want: []string{"SessionIdentityV1", "exact field/type/tag manifest"},
		},
		{
			name: "final MCP tool and scope types",
			mutateEvidence: func(source string) string {
				source = strings.Replace(source, "\tCaptureProvenance CaptureProvenanceV1 `json:\"capture_provenance\"`", "\tTool ToolID `json:\"tool\"`", 1)
				return strings.Replace(source, "\tScope RawSnapshotScopeV1 `json:\"scope\"`", "\tScope Scope `json:\"scope\"`", 1)
			},
			want: []string{"ReferenceV1", "exact field/type/tag manifest"},
		},
		{
			name: "stable JSON tag drift",
			mutateEvidence: func(source string) string {
				return strings.Replace(source, "`json:\"capture_provenance\"`", "`json:\"tool\"`", 1)
			},
			want: []string{"ReferenceV1", "exact field/type/tag manifest"},
		},
		{
			name: "pairing scope enum value",
			mutateEvidence: func(source string) string {
				return strings.Replace(source, "\tRawSnapshotScopeUnknown RawSnapshotScopeV1 = \"raw-unknown\"", "\tRawSnapshotScopeUnknown RawSnapshotScopeV1 = \"raw-unknown\"\n\tRawSnapshotScopePairing RawSnapshotScopeV1 = \"pairing-status\"", 1)
			},
			want: []string{"stable enum", "unexpected value", "RawSnapshotScopePairing"},
		},
		{
			name: "stable enum conversion bypass",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"", "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"\n\tEndpointRoleV1Converted EndpointRoleV1 = EndpointRoleV1(\"converted\")", 1)
			},
			want: []string{"stable enum", "unexpected value", "EndpointRoleV1Converted"},
		},
		{
			name: "stable enum concatenation bypass",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"", "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"\n\tEndpointRoleV1Concatenated EndpointRoleV1 = \"con\" + \"catenated\"", 1)
			},
			want: []string{"stable enum", "unexpected value", "EndpointRoleV1Concatenated"},
		},
		{
			name: "stable enum inherited declaration bypass",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"", "\tEndpointRoleV1Remote EndpointRoleV1 = \"remote\"\n\tEndpointRoleV1Inherited", 1)
			},
			want: []string{"stable enum", "unexpected value", "EndpointRoleV1Inherited"},
		},
		{
			name: "transitive trust authority field",
			mutateRaw: func(source string) string {
				return strings.Replace(source, "\tDigest string `json:\"digest,omitempty\"`\n}\n\ntype UnknownPath", "\tDigest string `json:\"digest,omitempty\"`\n\tTrustAuthority string `json:\"trust_authority,omitempty\"`\n}\n\ntype UnknownPath", 1)
			},
			want: []string{"RedactedID", "exact field/type/tag manifest"},
		},
		{
			name: "unexpected stable type",
			mutateRaw: func(source string) string {
				return source + "\ntype PairingAuthorityV1 struct{}\n"
			},
			want: []string{"unexpected stable contract type", "PairingAuthorityV1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newSyntheticRepository(t)
			rawSource := stableRawFixture()
			evidenceSource := stableEvidenceFixture()
			if test.mutateRaw != nil {
				rawSource = test.mutateRaw(rawSource)
			}
			if test.mutateEvidence != nil {
				evidenceSource = test.mutateEvidence(evidenceSource)
			}
			writeStableContractFixture(t, root, rawSource, evidenceSource)
			expectRejected(t, tool, root, test.want...)
		})
	}
}

func TestDocsCleanAPIBoundaryManifestArtifact(t *testing.T) {
	tool := buildAPIBoundary(t)
	root := newSyntheticRepository(t)
	writeFile(t, root, "eebusraw/api.go", `package eebusraw

type Envelope struct{ ID string }

func Observe(id string) Envelope { return Envelope{ID: id} }

const StateObserved = "observed"
`)
	runGit(t, root, nil, "add", "--", "eebusraw/api.go")

	artifactDir := t.TempDir()
	firstPath := filepath.Join(artifactDir, "api-boundary-1.json")
	firstOutput, err := runTool(t, tool, root, "-manifest", firstPath)
	if err != nil {
		t.Fatalf("API boundary extractor rejected valid fixture: %v\n%s", err, firstOutput)
	}
	first := readArtifact(t, firstPath)
	if strings.TrimSpace(firstOutput) != "" {
		t.Fatalf("artifact mode wrote unexpected stdout/stderr: %q", firstOutput)
	}

	writeFile(t, root, "eebusraw/api.go", `package eebusraw

const StateObserved = "observed"

func Observe(id string) Envelope { return Envelope{ID: id} }

type Envelope struct{ ID string }
`)
	runGit(t, root, nil, "add", "--", "eebusraw/api.go")
	secondPath := filepath.Join(artifactDir, "api-boundary-2.json")
	secondOutput, err := runTool(t, tool, root, "-manifest", secondPath)
	if err != nil {
		t.Fatalf("API boundary extractor rejected reordered fixture: %v\n%s", err, secondOutput)
	}
	second := readArtifact(t, secondPath)
	if !bytes.Equal(first, second) {
		t.Fatalf("manifest is not deterministic across declaration order\nfirst:  %s\nsecond: %s", first, second)
	}
	assertCanonicalManifest(t, first, root)
}

func TestDocsCleanAPIBoundaryRejectsUnsafeManifestDestinations(t *testing.T) {
	tool := buildAPIBoundary(t)
	tests := []struct {
		name        string
		destination func(*testing.T, string) string
	}{
		{
			name: "relative repository-local path",
			destination: func(_ *testing.T, _ string) string {
				return "api-boundary.json"
			},
		},
		{
			name: "absolute repository-local path",
			destination: func(_ *testing.T, root string) string {
				return filepath.Join(root, "api-boundary.json")
			},
		},
		{
			name: "external-looking path through symlink into repository",
			destination: func(t *testing.T, root string) string {
				alias := filepath.Join(t.TempDir(), "repository-alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(alias, "api-boundary.json")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newSyntheticRepository(t)
			output, err := runTool(t, tool, root, "-manifest", test.destination(t, root))
			if err == nil {
				t.Fatalf("API boundary extractor accepted an unsafe manifest destination")
			}
			for _, fragment := range []string{"artifact", "outside"} {
				if !strings.Contains(strings.ToLower(output), fragment) {
					t.Fatalf("unsafe manifest destination rejection omitted diagnostic %q\noutput:\n%s", fragment, output)
				}
			}
			manifestPath := filepath.Join(root, "api-boundary.json")
			if _, statErr := os.Lstat(manifestPath); statErr == nil {
				t.Fatalf("unsafe manifest destination created repository artifact %s", manifestPath)
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("inspect repository manifest destination: %v", statErr)
			}
		})
	}
}

type apiManifest struct {
	Schema          string                   `json:"schema"`
	Version         int                      `json:"version"`
	Module          string                   `json:"module"`
	Packages        []manifestPackage        `json:"packages"`
	StableContracts []manifestStableContract `json:"stable_contracts"`
}

type manifestPackage struct {
	ImportPath string           `json:"import_path"`
	Name       string           `json:"name"`
	Exports    []manifestExport `json:"exports"`
}

type manifestExport struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type manifestStableContract struct {
	Enums      []manifestStableEnum `json:"enums"`
	ImportPath string               `json:"import_path"`
	Types      []manifestStableType `json:"types"`
}

type manifestStableEnum struct {
	Type   string                    `json:"type"`
	Values []manifestStableEnumValue `json:"values"`
}

type manifestStableEnumValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type manifestStableType struct {
	Fields     []manifestStableField `json:"fields,omitempty"`
	Name       string                `json:"name"`
	Underlying string                `json:"underlying"`
}

type manifestStableField struct {
	JSONTag string `json:"json_tag"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

func assertCanonicalManifest(t *testing.T, data []byte, fixtureRoot string) {
	t.Helper()
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, data)
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		t.Fatalf("manifest is not canonical compact JSON with one trailing newline\nwant: %s\ngot:  %s", canonical, data)
	}
	if bytes.Contains(data, []byte(fixtureRoot)) {
		t.Fatalf("manifest leaks platform-specific absolute fixture path %q", fixtureRoot)
	}

	var manifest apiManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "helianthus.api-boundary-manifest" || manifest.Version != 1 {
		t.Fatalf("unexpected manifest identity: schema=%q version=%d", manifest.Schema, manifest.Version)
	}
	if manifest.Module != "example.test/registry" {
		t.Fatalf("unexpected module: %q", manifest.Module)
	}
	if len(manifest.Packages) != 1 {
		t.Fatalf("want one public package, got %#v", manifest.Packages)
	}
	pkg := manifest.Packages[0]
	if pkg.ImportPath != "example.test/registry/eebusraw" || pkg.Name != "eebusraw" {
		t.Fatalf("unexpected public package: %#v", pkg)
	}
	got := append([]manifestExport(nil), pkg.Exports...)
	sort.Slice(got, func(i, j int) bool {
		if got[i].Kind != got[j].Kind {
			return got[i].Kind < got[j].Kind
		}
		return got[i].Name < got[j].Name
	})
	want := []manifestExport{{Kind: "const", Name: "StateObserved"}, {Kind: "func", Name: "Observe"}, {Kind: "type", Name: "Envelope"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected exported API entries: got %#v want %#v", got, want)
	}
}

func writeStableContractFixture(t *testing.T, root, rawSource, evidenceSource string) {
	t.Helper()
	writeFile(t, root, "eebusraw/stable.go", rawSource)
	writeFile(t, root, "eebusevidence/stable.go", evidenceSource)
}

func stableRawFixture() string {
	return `package eebusraw

import "time"

type ContractVersion string

const IdentityContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.identity.v1alpha1"
const IdentityContractV1 ContractVersion = "helianthus.eebus.raw.identity.v1"

type MaskTier string

const MaskTierRedacted MaskTier = "redacted"

type EndpointRole string

const (
	EndpointRoleLocal EndpointRole = "local"
	EndpointRoleRemote EndpointRole = "remote"
)

type EndpointRoleV1 string

const (
	EndpointRoleV1Local EndpointRoleV1 = "local"
	EndpointRoleV1Remote EndpointRoleV1 = "remote"
)

type IDKind string

const (
	IDKindLocalSKI IDKind = "local-ski"
	IDKindRemoteSKI IDKind = "remote-ski"
	IDKindCertificateFingerprint IDKind = "certificate-fingerprint"
	IDKindPeer IDKind = "peer"
	IDKindSession IDKind = "session"
)

type RedactedID struct {
	Kind IDKind ` + "`json:\"kind\"`" + `
	Masked string ` + "`json:\"masked\"`" + `
	Digest string ` + "`json:\"digest,omitempty\"`" + `
}

type UnknownPath string

const (
	UnknownPathDocument UnknownPath = "/document/unknown"
	UnknownPathDevice UnknownPath = "/device/unknown"
	UnknownPathLocal UnknownPath = "/local/unknown"
	UnknownPathRemote UnknownPath = "/remote/unknown"
	UnknownPathSession UnknownPath = "/session/unknown"
)

type UnknownField struct {
	Path UnknownPath ` + "`json:\"path\"`" + `
	Value OpaqueValue ` + "`json:\"value\"`" + `
}

type OpaqueValue struct {
	Masked string ` + "`json:\"masked\"`" + `
	Digest string ` + "`json:\"digest,omitempty\"`" + `
	Size int ` + "`json:\"size,omitempty\"`" + `
}

type PairingObservation struct {
	State string ` + "`json:\"state,omitempty\"`" + `
}

type SessionState string

const SessionStateObserved SessionState = "observed"

type EndpointIdentity struct {
	Role EndpointRole ` + "`json:\"role\"`" + `
	ID RedactedID ` + "`json:\"id\"`" + `
	Pairing PairingObservation ` + "`json:\"pairing,omitempty\"`" + `
}

type SessionIdentity struct {
	ID RedactedID ` + "`json:\"id\"`" + `
	State SessionState ` + "`json:\"state\"`" + `
}

type EndpointIdentityV1 struct {
	Role EndpointRoleV1 ` + "`json:\"role\"`" + `
	ID RedactedID ` + "`json:\"id\"`" + `
	Unknown []UnknownField ` + "`json:\"unknown,omitempty\"`" + `
}

type SessionIdentityV1 struct {
	ID RedactedID ` + "`json:\"id\"`" + `
	RemoteID RedactedID ` + "`json:\"remote_id\"`" + `
	Unknown []UnknownField ` + "`json:\"unknown,omitempty\"`" + `
}

type IdentityDocumentV1 struct {
	Contract ContractVersion ` + "`json:\"contract\"`" + `
	MaskTier MaskTier ` + "`json:\"mask_tier\"`" + `
	CapturedAt time.Time ` + "`json:\"captured_at\"`" + `
	Local EndpointIdentityV1 ` + "`json:\"local\"`" + `
	Remotes []EndpointIdentityV1 ` + "`json:\"remotes,omitempty\"`" + `
	Sessions []SessionIdentityV1 ` + "`json:\"sessions,omitempty\"`" + `
	Unknown []UnknownField ` + "`json:\"unknown,omitempty\"`" + `
}
`
}

func stableEvidenceFixture() string {
	return `package eebusevidence

import (
	"time"

	"example.test/registry/eebusraw"
)

type ContractVersion string

const (
	EnvelopeContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1alpha1"
	EnvelopeContractV1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1"
)

type ToolID string

const ToolCapture ToolID = "eebus.v1.snapshot.capture"

type Scope string

const ScopePairingStatus Scope = "pairing-status"

type AuthScope string

const AuthScopeReadRaw AuthScope = "eebus.raw.read"

type ObjectKind string

const (
	ObjectKindIdentity ObjectKind = "identity"
	ObjectKindTopology ObjectKind = "topology"
	ObjectKindService ObjectKind = "service"
	ObjectKindSession ObjectKind = "session"
	ObjectKindUnknown ObjectKind = "unknown"
)

type CaptureProvenanceV1 string

const CaptureProvenanceRuntimeObservation CaptureProvenanceV1 = "runtime-observation"

type RawSnapshotScopeV1 string

const (
	RawSnapshotScopeRoot RawSnapshotScopeV1 = "raw-root"
	RawSnapshotScopeIdentity RawSnapshotScopeV1 = "raw-identity"
	RawSnapshotScopeTopology RawSnapshotScopeV1 = "raw-topology"
	RawSnapshotScopeServices RawSnapshotScopeV1 = "raw-services"
	RawSnapshotScopeSessions RawSnapshotScopeV1 = "raw-sessions"
	RawSnapshotScopeUnknown RawSnapshotScopeV1 = "raw-unknown"
)

type ReferenceV1 struct {
	Runtime eebusraw.RedactedID ` + "`json:\"runtime\"`" + `
	Contract ContractVersion ` + "`json:\"contract\"`" + `
	CaptureProvenance CaptureProvenanceV1 ` + "`json:\"capture_provenance\"`" + `
	Scope RawSnapshotScopeV1 ` + "`json:\"scope\"`" + `
	MaskTier eebusraw.MaskTier ` + "`json:\"mask_tier\"`" + `
	AuthScope AuthScope ` + "`json:\"auth_scope\"`" + `
}

type ObjectV1 struct {
	Kind ObjectKind ` + "`json:\"kind\"`" + `
	Digest string ` + "`json:\"digest\"`" + `
	Size int ` + "`json:\"size\"`" + `
	DataTimestamp time.Time ` + "`json:\"data_timestamp\"`" + `
	Unknown []eebusraw.UnknownField ` + "`json:\"unknown,omitempty\"`" + `
}

type EnvelopeV1 struct {
	Reference ReferenceV1 ` + "`json:\"ref\"`" + `
	CapturedAt time.Time ` + "`json:\"captured_at\"`" + `
	DataTimestamp time.Time ` + "`json:\"data_timestamp\"`" + `
	Objects []ObjectV1 ` + "`json:\"objects,omitempty\"`" + `
	DataHash string ` + "`json:\"data_hash,omitempty\"`" + `
}
`
}

func buildAPIBoundary(t *testing.T) string {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "apiboundary")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tool, ".")
	cmd.Env = testEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build API boundary command: %v\n%s", err, output)
	}
	return tool
}

func newSyntheticRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.test/registry\n\ngo 1.22.0\n")
	writeFile(t, root, "README.md", minimalREADME)
	writeFile(t, root, "AGENTS.md", "Synthetic repository instructions.\n")
	writeFile(t, root, "eebusraw/doc.go", "// Package eebusraw defines raw eeBUS data contracts.\npackage eebusraw\n")
	runGit(t, root, nil, "init", "-q")
	runGit(t, root, nil, "config", "core.ignorecase", "false")
	runGit(t, root, nil, "add", "--", ".")
	return root
}

func expectRejected(t *testing.T, tool, root string, want ...string) {
	t.Helper()
	output, err := runTool(t, tool, root)
	if err == nil {
		t.Fatalf("ownership/API gate accepted forbidden fixture; expected diagnostics containing %q", want)
	}
	for _, fragment := range want {
		if !strings.Contains(strings.ToLower(output), strings.ToLower(fragment)) {
			t.Fatalf("rejection omitted diagnostic %q\noutput:\n%s", fragment, output)
		}
	}
}

func replaceWithSymlink(t *testing.T, root, rel, target string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, nil, "add", "-f", "--", rel)
}

func runTool(t *testing.T, tool, root string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = root
	cmd.Env = testEnvironment()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runGit(t *testing.T, root string, input io.Reader, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Stdin = input
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readArtifact(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("API boundary manifest artifact was not created at %s: %v", path, err)
	}
	return data
}

func testEnvironment() []string {
	return append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
}
