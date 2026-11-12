package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/modfile"
)

const canonicalModulePath = "github.com/Project-Helianthus/helianthus-eebusreg"

var forbiddenExportFragments = []string{
	"Registry",
	"Projection",
	"Semantic",
	"Enbility",
	"Ship",
	"SHIP",
	"Spine",
	"SPINE",
	"EvidenceRef",
	"Dereference",
	"GraphQL",
	"Portal",
	"HomeAssistant",
	"CommandRoute",
	"CommandRouting",
	"TrustStore",
	"TrustMutation",
	"PairingWindow",
}

var allowedOperatorAdminMutationSurface = map[string]struct{}{
	"OpenPairingWindow":           {},
	"ClosePairingWindow":          {},
	"OpenPairingWindowRequestV1":  {},
	"ClosePairingWindowRequestV1": {},
	"TrustState":                  {},
}

var mutationVerbs = []string{
	"Register",
	"Unregister",
	"Set",
	"Accept",
	"Pair",
	"Authorize",
	"Trust",
	"Add",
	"Remove",
	"Delete",
	"Update",
	"Enable",
	"Disable",
	"Open",
	"Close",
	"Write",
	"Mutate",
}

var mutationNouns = []string{
	"RemoteSKI",
	"SKI",
	"Peer",
	"Remote",
	"Pairing",
	"Trust",
	"Trusted",
	"Certificate",
	"Fingerprint",
}

var forbiddenImports = []string{
	"github.com/Project-Helianthus/helianthus-ebusgateway",
	"github.com/Project-Helianthus/helianthus-ha-integration",
}

var allowedPublicPackages = map[string]string{
	".":             "eebusruntime",
	"eebusevidence": "eebusevidence",
	"eebusraw":      "eebusraw",
}

var allowedRuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "SnapshotContractV1"}:                        {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Unknown"}:             {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Stopped"}:             {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Starting"}:            {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Ready"}:               {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Degraded"}:            {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Shutdown"}:            {},
	{Kind: "const", Name: "DegradationReasonV1MissingDiscovery"}:       {},
	{Kind: "const", Name: "DegradationReasonV1DeniedTrust"}:            {},
	{Kind: "const", Name: "DegradationReasonV1RemoteDisconnect"}:       {},
	{Kind: "const", Name: "DegradationReasonV1CertificateUnavailable"}: {},
	{Kind: "const", Name: "DegradationReasonV1NoVisibleServices"}:      {},
	{Kind: "const", Name: "DegradationReasonV1NoData"}:                 {},
	{Kind: "const", Name: "ServiceKindV1Local"}:                        {},
	{Kind: "const", Name: "ServiceKindV1Remote"}:                       {},
	{Kind: "const", Name: "ObservedSessionStateV1Unknown"}:             {},
	{Kind: "const", Name: "ObservedSessionStateV1Connecting"}:          {},
	{Kind: "const", Name: "ObservedSessionStateV1Connected"}:           {},
	{Kind: "const", Name: "ObservedSessionStateV1Disconnected"}:        {},
	{Kind: "const", Name: "ObservedSessionStateV1Degraded"}:            {},
	{Kind: "const", Name: "FeatureRoleV1Unspecified"}:                  {},
	{Kind: "const", Name: "FeatureRoleV1Client"}:                       {},
	{Kind: "const", Name: "FeatureRoleV1Server"}:                       {},
	{Kind: "const", Name: "PairingPolicyClosed"}:                       {},
	{Kind: "const", Name: "AdminViewV1Trusted"}:                        {},
	{Kind: "const", Name: "AdminViewV1Connected"}:                      {},
	{Kind: "const", Name: "AdminViewV1Discovered"}:                     {},
	{Kind: "const", Name: "AdminViewV1Candidate"}:                      {},
	{Kind: "const", Name: "AdminErrorCodeV1AdminBoundaryUnavailable"}:  {},
	{Kind: "const", Name: "AdminErrorCodeV1Unauthenticated"}:           {},
	{Kind: "const", Name: "AdminErrorCodeV1Forbidden"}:                 {},
	{Kind: "const", Name: "AdminErrorCodeV1CSRFRejected"}:              {},
	{Kind: "const", Name: "AdminErrorCodeV1InvalidRequest"}:            {},
	{Kind: "const", Name: "AdminErrorCodeV1StateConflict"}:             {},
	{Kind: "const", Name: "AdminErrorCodeV1SnapshotExpired"}:           {},
	{Kind: "const", Name: "AdminErrorCodeV1IdempotencyConflict"}:       {},
	{Kind: "const", Name: "AdminErrorCodeV1PairingClosed"}:             {},
	{Kind: "const", Name: "AdminErrorCodeV1ObservationStale"}:          {},
	{Kind: "const", Name: "AdminErrorCodeV1IdentityMismatch"}:          {},
	{Kind: "const", Name: "AdminErrorCodeV1AssociationIncomplete"}:     {},
	{Kind: "const", Name: "AdminErrorCodeV1CandidateExpired"}:          {},
	{Kind: "const", Name: "AdminErrorCodeV1CandidateBusy"}:             {},
	{Kind: "const", Name: "AdminErrorCodeV1TrustDenied"}:               {},
	{Kind: "const", Name: "AdminErrorCodeV1ListenerUnavailable"}:       {},
	{Kind: "const", Name: "AdminErrorCodeV1DiscoveryUnavailable"}:      {},
	{Kind: "const", Name: "AdminErrorCodeV1AttemptTimeout"}:            {},
	{Kind: "const", Name: "AdminErrorCodeV1Disconnected"}:              {},
	{Kind: "const", Name: "AdminErrorCodeV1BackoffActive"}:             {},
	{Kind: "const", Name: "AdminErrorCodeV1TerminalQuarantine"}:        {},
	{Kind: "const", Name: "AdminErrorCodeV1PersistenceFailure"}:        {},
	{Kind: "const", Name: "AdminErrorCodeV1UnknownState"}:              {},
	{Kind: "func", Name: "AdminErrorV1.Error"}:                         {},
	{Kind: "func", Name: "BuildRedactedSnapshotV1"}:                    {},
	{Kind: "func", Name: "MetadataV1.MarshalJSON"}:                     {},
	{Kind: "func", Name: "MetadataV1.UnmarshalJSON"}:                   {},
	{Kind: "func", Name: "MetadataValueV1.MarshalJSON"}:                {},
	{Kind: "func", Name: "MetadataValueV1.UnmarshalJSON"}:              {},
	{Kind: "func", Name: "New"}:                                        {},
	{Kind: "func", Name: "NewOperatorRuntimeV1"}:                       {},
	{Kind: "func", Name: "NewSnapshotV1"}:                              {},
	{Kind: "func", Name: "OpaqueScalarV1.MarshalJSON"}:                 {},
	{Kind: "func", Name: "OpaqueScalarV1.UnmarshalJSON"}:               {},
	{Kind: "func", Name: "OpaqueValueV1.MarshalJSON"}:                  {},
	{Kind: "func", Name: "OpaqueValueV1.UnmarshalJSON"}:                {},
	{Kind: "func", Name: "RedactedSnapshotV1.Format"}:                  {},
	{Kind: "func", Name: "RedactedSnapshotV1.GoString"}:                {},
	{Kind: "func", Name: "RedactedSnapshotV1.MarshalJSON"}:             {},
	{Kind: "func", Name: "RedactedSnapshotV1.String"}:                  {},
	{Kind: "func", Name: "RedactedSnapshotV1.UnmarshalJSON"}:           {},
	{Kind: "func", Name: "RedactedSnapshotV1.Validate"}:                {},
	{Kind: "func", Name: "RawMutationOutcomeV1.Clone"}:                 {},
	{Kind: "func", Name: "SnapshotV1.Clone"}:                           {},
	{Kind: "func", Name: "SnapshotV1.ComputeDataHash"}:                 {},
	{Kind: "func", Name: "SnapshotV1.Format"}:                          {},
	{Kind: "func", Name: "SnapshotV1.GoString"}:                        {},
	{Kind: "func", Name: "SnapshotV1.MarshalJSON"}:                     {},
	{Kind: "func", Name: "SnapshotV1.String"}:                          {},
	{Kind: "func", Name: "SnapshotV1.Validate"}:                        {},
	{Kind: "type", Name: "DegradationReasonV1"}:                        {},
	{Kind: "type", Name: "DegradationV1"}:                              {},
	{Kind: "type", Name: "DeviceV1"}:                                   {},
	{Kind: "type", Name: "EntityV1"}:                                   {},
	{Kind: "type", Name: "FeatureRoleV1"}:                              {},
	{Kind: "type", Name: "FeatureV1"}:                                  {},
	{Kind: "type", Name: "MetadataV1"}:                                 {},
	{Kind: "type", Name: "MetadataValueV1"}:                            {},
	{Kind: "type", Name: "ObservedRuntimeStateV1"}:                     {},
	{Kind: "type", Name: "ObservedSessionStateV1"}:                     {},
	{Kind: "type", Name: "OpaqueObservationV1"}:                        {},
	{Kind: "type", Name: "OpaqueScalarV1"}:                             {},
	{Kind: "type", Name: "OpaqueValueV1"}:                              {},
	{Kind: "type", Name: "PairingObservationV1"}:                       {},
	{Kind: "type", Name: "PairingPolicy"}:                              {},
	{Kind: "type", Name: "RedactedDeviceV1"}:                           {},
	{Kind: "type", Name: "RedactedEntityV1"}:                           {},
	{Kind: "type", Name: "RedactedFeatureV1"}:                          {},
	{Kind: "type", Name: "RedactedServiceV1"}:                          {},
	{Kind: "type", Name: "RedactedSessionV1"}:                          {},
	{Kind: "type", Name: "RedactedSnapshotMetaV1"}:                     {},
	{Kind: "type", Name: "RedactedSnapshotV1"}:                         {},
	{Kind: "type", Name: "RedactedUseCaseV1"}:                          {},
	{Kind: "type", Name: "Remote"}:                                     {},
	{Kind: "type", Name: "RawFeatureRuntimeV1"}:                        {},
	{Kind: "type", Name: "RawMutationOutcomeV1"}:                       {},
	{Kind: "type", Name: "RawMutationRuntimeV1"}:                       {},
	{Kind: "type", Name: "Runtime"}:                                    {},
	{Kind: "type", Name: "RuntimeObservationV1"}:                       {},
	{Kind: "type", Name: "ServiceKindV1"}:                              {},
	{Kind: "type", Name: "ServiceV1"}:                                  {},
	{Kind: "type", Name: "SessionV1"}:                                  {},
	{Kind: "type", Name: "SnapshotMetaV1"}:                             {},
	{Kind: "type", Name: "SnapshotV1"}:                                 {},
	{Kind: "type", Name: "UseCaseV1"}:                                  {},
	{Kind: "type", Name: "Config"}:                                     {},
	{Kind: "type", Name: "AdminV1"}:                                    {},
	{Kind: "type", Name: "AdminErrorCodeV1"}:                           {},
	{Kind: "type", Name: "AdminErrorV1"}:                               {},
	{Kind: "type", Name: "AdminOutcomeV1"}:                             {},
	{Kind: "type", Name: "AdminViewV1"}:                                {},
	{Kind: "type", Name: "AdminSnapshotV1"}:                            {},
	{Kind: "type", Name: "AdminSnapshotRequestV1"}:                     {},
	{Kind: "type", Name: "AdminMutationResultV1"}:                      {},
	{Kind: "type", Name: "AdminSelectionResultV1"}:                     {},
	{Kind: "type", Name: "MutationPreconditionV1"}:                     {},
	{Kind: "type", Name: "OpenPairingWindowRequestV1"}:                 {},
	{Kind: "type", Name: "ClosePairingWindowRequestV1"}:                {},
	{Kind: "type", Name: "SelectRequestV1"}:                            {},
	{Kind: "type", Name: "ConnectRequestV1"}:                           {},
	{Kind: "type", Name: "ConfirmRequestV1"}:                           {},
	{Kind: "type", Name: "CancelRequestV1"}:                            {},
	{Kind: "type", Name: "RetryTrustedRequestV1"}:                      {},
	{Kind: "type", Name: "UntrustRequestV1"}:                           {},
	{Kind: "type", Name: "CandidateHandleV1"}:                          {},
	{Kind: "type", Name: "ConnectedPartnerV1"}:                         {},
	{Kind: "type", Name: "DiscoveredPartnerV1"}:                        {},
	{Kind: "type", Name: "ObservationHandleV1"}:                        {},
	{Kind: "type", Name: "CandidateV1"}:                                {},
	{Kind: "type", Name: "PartnerHandleV1"}:                            {},
	{Kind: "type", Name: "SelectionHandleV1"}:                          {},
	{Kind: "type", Name: "TrustedPartnerV1"}:                           {},
	{Kind: "var", Name: "ErrRuntimeDisabled"}:                          {},
	{Kind: "var", Name: "ErrRuntimeShutdown"}:                          {},
}

var postM9OperatorAdminRuntimeExports = loadExportInventory("operator_admin_runtime.txt")

var issue137ParticipationExports = loadExportInventory("participation.txt")

var issue137ParticipationOperatorExports = loadExportInventory("participation_operator.txt")

func init() {
	for exported := range issue137ParticipationExports {
		allowedRuntimeExports[exported] = struct{}{}
	}
	for exported := range postM9OperatorAdminRuntimeExports {
		allowedRuntimeExports[exported] = struct{}{}
	}
	for exported := range issue129NativeRuntimeV2Exports {
		allowedRuntimeExports[exported] = struct{}{}
	}
}

var issue129NativeRuntimeV2Exports = loadExportInventory("native_runtime_second.txt")

var msp055RuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "PairingPolicyClosed"}:       {},
	{Kind: "func", Name: "New"}:                        {},
	{Kind: "func", Name: "RawMutationOutcomeV1.Clone"}: {},
	{Kind: "type", Name: "Config"}:                     {},
	{Kind: "type", Name: "PairingPolicy"}:              {},
	{Kind: "type", Name: "RawFeatureRuntimeV1"}:        {},
	{Kind: "type", Name: "RawMutationOutcomeV1"}:       {},
	{Kind: "type", Name: "RawMutationRuntimeV1"}:       {},
	{Kind: "type", Name: "Remote"}:                     {},
	{Kind: "type", Name: "Runtime"}:                    {},
	{Kind: "var", Name: "ErrRuntimeDisabled"}:          {},
	{Kind: "var", Name: "ErrRuntimeShutdown"}:          {},
}

var allowedMSP035RawExports = loadExportInventory("raw.txt")

var allowedMSP0625RawExports = loadExportInventory("raw_feature.txt")

var allowedMSP0625MutationRawExports = loadExportInventory("raw_mutation.txt")

var allowedCurrentRawExports = mergeExportInventories(
	allowedMSP035RawExports,
	allowedMSP0625RawExports,
	allowedMSP0625MutationRawExports,
)

var allowedMSP035EvidenceExports = loadExportInventory("evidence.txt")

var documentationExtensions = map[string]struct{}{
	".adoc":     {},
	".asciidoc": {},
	".markdown": {},
	".md":       {},
	".mdown":    {},
	".mkd":      {},
	".rst":      {},
	".txt":      {},
}

var documentationNames = map[string]struct{}{
	"api":          {},
	"architecture": {},
	"changelog":    {},
	"contributing": {},
	"design":       {},
	"governance":   {},
	"protocol":     {},
	"readme":       {},
	"roadmap":      {},
	"security":     {},
	"support":      {},
}

type apiManifest struct {
	Module          string                   `json:"module"`
	Packages        []manifestPackage        `json:"packages"`
	Schema          string                   `json:"schema"`
	StableContracts []manifestStableContract `json:"stable_contracts"`
	Version         int                      `json:"version"`
}

type manifestPackage struct {
	Exports    []manifestExport `json:"exports"`
	ImportPath string           `json:"import_path"`
	Name       string           `json:"name"`
}

type manifestExport struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func mergeExportInventories(inventories ...map[manifestExport]struct{}) map[manifestExport]struct{} {
	result := make(map[manifestExport]struct{})
	for _, inventory := range inventories {
		for exported := range inventory {
			result[exported] = struct{}{}
		}
	}
	return result
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

type packageInventory struct {
	exports map[manifestExport]struct{}
	files   []*ast.File
	name    string
	types   map[string]typeDeclaration
}

type typeDeclaration struct {
	imports map[string]string
	rel     string
	spec    *ast.TypeSpec
}

type constantDeclaration struct {
	name     string
	rel      string
	typeName string
	value    string
}

type sourceImporter struct {
	checked     map[string]*types.Package
	checking    map[string]bool
	fallback    types.Importer
	fset        *token.FileSet
	inventories map[string]*packageInventory
}

type chainedImporter struct {
	primary   types.Importer
	secondary types.Importer
}

type stableContractSpec struct {
	enums      []stableEnumSpec
	importPath string
	optional   bool
	types      []manifestStableType
	root       string
}

type stableEnumSpec struct {
	additional []string
	exact      bool
	typeName   string
	values     []manifestStableEnumValue
}

type pathOrigin string

const (
	trackedPath    pathOrigin = "tracked"
	untrackedPath  pathOrigin = "untracked"
	ignoredPath    pathOrigin = "ignored"
	filesystemPath pathOrigin = "filesystem"
)

func main() {
	flags := flag.NewFlagSet("apiboundary", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "write the API boundary manifest outside the repository")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fatal(err)
	}

	violations, err := validateRepositoryPaths(root)
	if err != nil {
		fatal(err)
	}
	manifest := apiManifest{}
	if len(violations) == 0 {
		var goViolations []string
		manifest, goViolations, err = inspectGoBoundary(root)
		if err != nil {
			fatal(err)
		}
		violations = append(violations, goViolations...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, violation)
		}
		os.Exit(1)
	}

	if *manifestPath == "" {
		return
	}
	destination, err := safeManifestDestination(root, *manifestPath)
	if err != nil {
		fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		fatal(fmt.Errorf("marshal API boundary artifact: %w", err))
	}
	data = append(data, '\n')
	if err := writeAtomic(destination, data); err != nil {
		fatal(fmt.Errorf("write API boundary artifact: %w", err))
	}
}

func validateRepositoryPaths(root string) ([]string, error) {
	paths := make(map[string]map[pathOrigin]struct{})
	lists := []struct {
		origin pathOrigin
		args   []string
	}{
		{origin: trackedPath, args: []string{"ls-files", "-z"}},
		{origin: untrackedPath, args: []string{"ls-files", "--others", "--exclude-standard", "-z"}},
		{origin: ignoredPath, args: []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}},
	}
	for _, list := range lists {
		entries, err := gitNullList(root, list.args...)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			addPathOrigin(paths, filepath.ToSlash(entry), list.origin)
		}
	}

	violations := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		if _, known := paths[rel]; !known {
			addPathOrigin(paths, rel, filesystemPath)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("inspect symlink %s: %w", rel, err)
			}
			violations[symlinkViolation(rel, target)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	stagedSymlinks, err := gitStagedSymlinks(root)
	if err != nil {
		return nil, err
	}
	for path, target := range stagedSymlinks {
		violations[symlinkViolation(path, target)] = struct{}{}
	}

	casefold := make(map[string]string)
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		if err := validateRelativePath(path); err != nil {
			violations[fmt.Sprintf("unsafe repository path %q: %v", path, err)] = struct{}{}
		}
		folded := strings.ToLower(path)
		if prior, ok := casefold[folded]; ok && prior != path {
			violations[fmt.Sprintf("casefold collision: %s conflicts with %s", path, prior)] = struct{}{}
		} else {
			casefold[folded] = path
		}
		for origin := range paths[path] {
			if hasFoldedPathSegment(path, "docs") {
				violations[fmt.Sprintf("%s docs path is forbidden: %s", origin, path)] = struct{}{}
			}
			if isDocumentationPath(path) && path != "README.md" && path != "AGENTS.md" && !isExportInventoryPath(path) {
				violations[fmt.Sprintf("%s markdown/documentation path is outside the allowlist: %s", origin, path)] = struct{}{}
			}
		}
	}

	readmePath := filepath.Join(root, "README.md")
	info, err := os.Lstat(readmePath)
	if err != nil {
		if os.IsNotExist(err) {
			violations["README.md must exist and be non-empty"] = struct{}{}
		} else {
			return nil, err
		}
	} else if info.Mode().IsRegular() {
		data, err := os.ReadFile(readmePath)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(data)) == "" {
			violations["README.md must be non-empty"] = struct{}{}
		}
	} else {
		violations["README.md must be a non-empty regular file"] = struct{}{}
	}

	result := make([]string, 0, len(violations))
	for violation := range violations {
		result = append(result, violation)
	}
	sort.Strings(result)
	return result, nil
}

func inspectGoBoundary(root string) (apiManifest, []string, error) {
	moduleData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return apiManifest{}, nil, fmt.Errorf("read module identity: %w", err)
	}
	moduleFile, err := modfile.Parse("go.mod", moduleData, nil)
	if err != nil {
		return apiManifest{}, nil, fmt.Errorf("read module identity: %w", err)
	}
	if moduleFile.Module == nil || moduleFile.Module.Mod.Path == "" {
		return apiManifest{}, nil, fmt.Errorf("read module identity: go.mod has no module directive")
	}
	modulePath := moduleFile.Module.Mod.Path
	runtimeLifecycleActive := false
	for _, required := range moduleFile.Require {
		if required.Mod.Path == "github.com/Project-Helianthus/helianthus-eebus-go" {
			runtimeLifecycleActive = true
			break
		}
	}

	fset := token.NewFileSet()
	packages := make(map[string]*packageInventory)
	var violations []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch {
			case entry.Name() == ".git", entry.Name() == "vendor", strings.EqualFold(entry.Name(), "docs"):
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: parse error: %v", rel, err))
			return nil
		}

		internal := hasPathSegment(rel, "internal")
		testFile := strings.HasSuffix(rel, "_test.go")
		if !testFile {
			checkBuildConstraints(fset, rel, file, &violations)
		}
		if !internal && !testFile {
			directory := filepath.ToSlash(filepath.Dir(rel))
			allowedName, allowed := allowedPublicPackages[directory]
			if !allowed || file.Name.Name != allowedName {
				violations = append(violations, fmt.Sprintf("%s: unexpected public package %s", rel, file.Name.Name))
			} else {
				importPath := modulePath
				if directory != "." {
					importPath += "/" + directory
				}
				inventory := packages[importPath]
				if inventory == nil {
					inventory = &packageInventory{
						exports: make(map[manifestExport]struct{}),
						name:    file.Name.Name,
						types:   make(map[string]typeDeclaration),
					}
					packages[importPath] = inventory
				}
				inventory.files = append(inventory.files, file)
				collectExports(file, inventory.exports)
				collectDeclarations(rel, file, inventory)
			}
		}

		internalAliases := internalImportAliases(file, modulePath)
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !internal && isProtocolImplementationImport(importPath) {
				violations = append(violations, at(fset, imp.Pos(), rel, "direct protocol implementation imports are allowed only under internal/"))
			}
			rootRuntimeImplementation := !internal && !testFile && filepath.ToSlash(filepath.Dir(rel)) == "." && file.Name.Name == "eebusruntime"
			facadeImplementationImport := rootRuntimeImplementation && importPath == modulePath+"/internal/eebusfacade" &&
				(imp.Name == nil || imp.Name.Name != ".")
			if !internal && !testFile && strings.HasPrefix(importPath, modulePath+"/internal/") && !facadeImplementationImport {
				violations = append(violations, at(fset, imp.Pos(), rel, "public API packages must not import internal implementation types"))
			}
			for _, forbidden := range forbiddenImports {
				if strings.Contains(importPath, forbidden) {
					violations = append(violations, at(fset, imp.Pos(), rel, "gateway or consumer imports are not allowed in this repo"))
				}
			}
		}
		if internal {
			return nil
		}
		if !testFile {
			checkCrossRuntimeStrings(fset, rel, file, &violations)
		}
		for _, decl := range file.Decls {
			switch declaration := decl.(type) {
			case *ast.FuncDecl:
				if declaration.Name != nil {
					checkExportedName(fset, rel, declaration.Name, &violations)
					if declaration.Name.IsExported() {
						checkExportedTypeSurface(fset, rel, declaration.Type, &violations)
						checkInternalTypeEscape(fset, rel, declaration.Type, internalAliases, &violations)
					}
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch typed := spec.(type) {
					case *ast.TypeSpec:
						checkExportedName(fset, rel, typed.Name, &violations)
						if typed.Name.IsExported() {
							checkExportedTypeSurface(fset, rel, typed.Type, &violations)
							checkInternalTypeEscape(fset, rel, typed.Type, internalAliases, &violations)
						}
					case *ast.ValueSpec:
						exported := false
						for _, name := range typed.Names {
							checkExportedName(fset, rel, name, &violations)
							exported = exported || name.IsExported()
						}
						if exported {
							checkInternalTypeEscape(fset, rel, typed, internalAliases, &violations)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return apiManifest{}, nil, err
	}

	stableContracts, stableViolations := inspectStableContracts(root, modulePath, fset, packages)
	violations = append(violations, stableViolations...)
	violations = append(violations, inspectRuntimeExports(modulePath, packages, runtimeLifecycleActive)...)
	violations = append(violations, inspectMSP035DependencyExports(modulePath, packages)...)
	manifest := apiManifest{
		Module:          modulePath,
		Packages:        make([]manifestPackage, 0, len(packages)),
		Schema:          "helianthus.api-boundary-manifest",
		StableContracts: stableContracts,
		Version:         1,
	}
	for importPath, inventory := range packages {
		pkg := manifestPackage{
			Exports:    make([]manifestExport, 0, len(inventory.exports)),
			ImportPath: importPath,
			Name:       inventory.name,
		}
		for exported := range inventory.exports {
			pkg.Exports = append(pkg.Exports, exported)
		}
		sort.Slice(pkg.Exports, func(i, j int) bool {
			if pkg.Exports[i].Kind != pkg.Exports[j].Kind {
				return pkg.Exports[i].Kind < pkg.Exports[j].Kind
			}
			return pkg.Exports[i].Name < pkg.Exports[j].Name
		})
		manifest.Packages = append(manifest.Packages, pkg)
	}
	sort.Slice(manifest.Packages, func(i, j int) bool {
		return manifest.Packages[i].ImportPath < manifest.Packages[j].ImportPath
	})
	return manifest, violations, nil
}

func isProtocolImplementationImport(path string) bool {
	for _, prefix := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go",
		"github.com/Project-Helianthus/helianthus-ship-go",
		"github.com/Project-Helianthus/helianthus-spine-go",
		"github.com/enbility/eebus-go",
		"github.com/enbility/ship-go",
		"github.com/enbility/spine-go",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func inspectRuntimeExports(modulePath string, packages map[string]*packageInventory, lifecycleActive bool) []string {
	inventory := packages[modulePath]
	if inventory == nil {
		if modulePath == canonicalModulePath {
			return []string{"root eebusruntime package is missing"}
		}
		return nil
	}
	if modulePath != canonicalModulePath {
		if _, active := inventory.types["SnapshotV1"]; !active {
			return nil
		}
	}
	snapshotOnlyContract := modulePath == canonicalModulePath && !lifecycleActive
	var violations []string
	for actual := range inventory.exports {
		if _, allowed := allowedRuntimeExports[actual]; !allowed {
			violations = append(violations, fmt.Sprintf("root eebusruntime export is outside the MSP-036 closed inventory: %s %s", actual.Kind, actual.Name))
		}
	}
	for expected := range allowedRuntimeExports {
		if snapshotOnlyContract {
			if _, participation := issue137ParticipationExports[expected]; participation {
				continue
			}
			if _, lifecycle := msp055RuntimeExports[expected]; lifecycle {
				continue
			}
			if _, operatorAdmin := postM9OperatorAdminRuntimeExports[expected]; operatorAdmin {
				continue
			}
			if _, nativeV2 := issue129NativeRuntimeV2Exports[expected]; nativeV2 {
				continue
			}
		}
		if _, present := inventory.exports[expected]; !present {
			violations = append(violations, fmt.Sprintf("root eebusruntime export required by MSP-036 is missing: %s %s", expected.Kind, expected.Name))
		}
	}
	return violations
}

func inspectMSP035DependencyExports(modulePath string, packages map[string]*packageInventory) []string {
	if modulePath != canonicalModulePath {
		return nil
	}
	dependencies := []struct {
		name    string
		path    string
		exports map[manifestExport]struct{}
	}{
		{name: "eebusraw", path: modulePath + "/eebusraw", exports: allowedCurrentRawExports},
		{name: "eebusevidence", path: modulePath + "/eebusevidence", exports: allowedMSP035EvidenceExports},
	}
	var violations []string
	for _, dependency := range dependencies {
		inventory := packages[dependency.path]
		if inventory == nil {
			violations = append(violations, fmt.Sprintf("MSP-035 dependency %s public package is missing", dependency.name))
			continue
		}
		for actual := range inventory.exports {
			if _, allowed := dependency.exports[actual]; !allowed {
				violations = append(violations, fmt.Sprintf("MSP-035 dependency %s public export is outside the frozen inventory: %s %s", dependency.name, actual.Kind, actual.Name))
			}
		}
		for expected := range dependency.exports {
			if _, present := inventory.exports[expected]; !present {
				violations = append(violations, fmt.Sprintf("MSP-035 dependency %s frozen public export is missing: %s %s", dependency.name, expected.Kind, expected.Name))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func checkBuildConstraints(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	modernConstraints := 0
	for _, group := range file.Comments {
		for _, comment := range group.List {
			modern := constraint.IsGoBuild(comment.Text)
			if !modern && !constraint.IsPlusBuild(comment.Text) {
				continue
			}
			if modern {
				modernConstraints++
			}
			expression, err := constraint.Parse(comment.Text)
			valid := err == nil && comment.End() < file.Package &&
				fset.PositionFor(file.Package, false).Line >= fset.PositionFor(group.End(), false).Line+2
			if modern && err == nil {
				valid = valid && comment.Text == "//go:build "+expression.String()
			}
			if !valid {
				*violations = append(*violations, at(fset, comment.Pos(), rel, "invalid build constraint comment"))
			}
		}
	}
	if modernConstraints > 1 {
		*violations = append(*violations, at(fset, file.Package, rel, "multiple go:build constraint comments"))
	}
}

func collectExports(file *ast.File, exports map[manifestExport]struct{}) {
	for _, decl := range file.Decls {
		switch declaration := decl.(type) {
		case *ast.FuncDecl:
			if declaration.Name == nil || !declaration.Name.IsExported() {
				continue
			}
			name := declaration.Name.Name
			if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
				receiver := receiverName(declaration.Recv.List[0].Type)
				if receiver == "" || !ast.IsExported(receiver) {
					continue
				}
				name = receiver + "." + name
			}
			exports[manifestExport{Kind: "func", Name: name}] = struct{}{}
		case *ast.GenDecl:
			kind := ""
			switch declaration.Tok {
			case token.CONST:
				kind = "const"
			case token.TYPE:
				kind = "type"
			case token.VAR:
				kind = "var"
			}
			if kind == "" {
				continue
			}
			for _, spec := range declaration.Specs {
				switch typed := spec.(type) {
				case *ast.TypeSpec:
					if typed.Name.IsExported() {
						exports[manifestExport{Kind: kind, Name: typed.Name.Name}] = struct{}{}
					}
				case *ast.ValueSpec:
					for _, name := range typed.Names {
						if name.IsExported() {
							exports[manifestExport{Kind: kind, Name: name.Name}] = struct{}{}
						}
					}
				}
			}
		}
	}
}

func collectDeclarations(rel string, file *ast.File, inventory *packageInventory) {
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gen.Tok {
		case token.TYPE:
			for _, rawSpec := range gen.Specs {
				spec, ok := rawSpec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				inventory.types[spec.Name.Name] = typeDeclaration{imports: imports, rel: rel, spec: spec}
			}
		}
	}
}

func inspectStableContracts(root, modulePath string, fset *token.FileSet, packages map[string]*packageInventory) ([]manifestStableContract, []string) {
	specs := stableContractSpecs(modulePath)
	active := modulePath == canonicalModulePath
	if !active {
		for _, spec := range specs {
			if inventory := packages[spec.importPath]; inventory != nil {
				if _, ok := inventory.types[spec.root]; ok {
					active = true
					break
				}
			}
		}
	}
	if !active {
		return []manifestStableContract{}, nil
	}

	checkedPackages, typeViolations := typeCheckStablePackages(root, fset, packages, specs)
	contracts := make([]manifestStableContract, 0, len(specs))
	violations := append([]string(nil), typeViolations...)
	allExpectedTypes := make(map[string]map[string]struct{})
	for _, spec := range specs {
		expected := allExpectedTypes[spec.importPath]
		if expected == nil {
			expected = make(map[string]struct{})
			allExpectedTypes[spec.importPath] = expected
		}
		for _, stableType := range spec.types {
			expected[stableType.Name] = struct{}{}
		}
	}
	allExpectedTypes[modulePath]["RawFeatureRuntimeV1"] = struct{}{}
	allExpectedTypes[modulePath]["RawMutationOutcomeV1"] = struct{}{}
	allExpectedTypes[modulePath]["RawMutationRuntimeV1"] = struct{}{}
	for _, spec := range specs {
		inventory := packages[spec.importPath]
		if spec.optional && (inventory == nil || inventory.types[spec.root].spec == nil) {
			continue
		}
		if modulePath != canonicalModulePath {
			if inventory == nil {
				continue
			}
			if _, ok := inventory.types[spec.root]; !ok {
				continue
			}
		}
		contract := manifestStableContract{
			Enums:      make([]manifestStableEnum, 0, len(spec.enums)),
			ImportPath: spec.importPath,
			Types:      append([]manifestStableType(nil), spec.types...),
		}
		inventory = packages[spec.importPath]
		if inventory == nil {
			violations = append(violations, "stable contract package is missing: "+spec.importPath)
			contracts = append(contracts, contract)
			continue
		}
		constants := typedConstants(root, fset, checkedPackages[spec.importPath])

		expectedTypes := make(map[string]manifestStableType, len(spec.types))
		for _, expected := range spec.types {
			expectedTypes[expected.Name] = expected
			declaration, ok := inventory.types[expected.Name]
			if !ok {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s is missing", spec.importPath, expected.Name))
				continue
			}
			actual, err := inspectStableType(declaration)
			if err != nil {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s: %v", declaration.rel, expected.Name, err))
				continue
			}
			if !stableContractTypeMatches(actual, expected) {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s does not match exact field/type/tag manifest: got %s want %s", declaration.rel, expected.Name, stableTypeText(actual), stableTypeText(expected)))
			}
		}
		for name := range inventory.types {
			if ast.IsExported(name) && strings.HasSuffix(name, "V1") {
				if _, operatorAdmin := postM9OperatorAdminRuntimeExports[manifestExport{Kind: "type", Name: name}]; operatorAdmin {
					continue
				}
				if _, participation := issue137ParticipationOperatorExports[manifestExport{Kind: "type", Name: name}]; participation {
					continue
				}
				if _, ok := allExpectedTypes[spec.importPath][name]; !ok {
					violations = append(violations, fmt.Sprintf("%s: unexpected stable contract type %s", spec.importPath, name))
				}
			}
		}

		for _, enumSpec := range spec.enums {
			enum := manifestStableEnum{Type: enumSpec.typeName, Values: append([]manifestStableEnumValue(nil), enumSpec.values...)}
			contract.Enums = append(contract.Enums, enum)
			expectedNames := make(map[string]manifestStableEnumValue, len(enumSpec.values))
			for _, expected := range enumSpec.values {
				expectedNames[expected.Name] = expected
				actual, ok := constants[expected.Name]
				if !ok {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s is missing", spec.importPath, enumSpec.typeName, expected.Name))
					continue
				}
				if actual.typeName != enumSpec.typeName || actual.value != expected.Value {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s does not match exact manifest", actual.rel, enumSpec.typeName, expected.Name))
				}
			}
			if enumSpec.exact {
				for name, actual := range constants {
					if !ast.IsExported(name) || actual.typeName != enumSpec.typeName {
						continue
					}
					if _, ok := expectedNames[name]; !ok {
						if slices.Contains(enumSpec.additional, name) {
							continue
						}
						violations = append(violations, fmt.Sprintf("%s: stable enum %s has unexpected value %s", actual.rel, enumSpec.typeName, name))
					}
				}
			}
		}
		sort.Slice(contract.Types, func(i, j int) bool { return contract.Types[i].Name < contract.Types[j].Name })
		sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].Type < contract.Enums[j].Type })
		for i := range contract.Enums {
			sort.Slice(contract.Enums[i].Values, func(left, right int) bool {
				return contract.Enums[i].Values[left].Name < contract.Enums[i].Values[right].Name
			})
		}
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ImportPath < contracts[j].ImportPath })
	return contracts, violations
}

func typeCheckStablePackages(root string, fset *token.FileSet, inventories map[string]*packageInventory, specs []stableContractSpec) (map[string]*types.Package, []string) {
	fallback := types.Importer(importer.Default())
	if len(specs) != 0 {
		fallback = &chainedImporter{
			primary:   fallback,
			secondary: moduleExportImporter(fset, root),
		}
	}
	loader := &sourceImporter{
		checked:     make(map[string]*types.Package),
		checking:    make(map[string]bool),
		fallback:    fallback,
		fset:        fset,
		inventories: inventories,
	}
	var violations []string
	for _, spec := range specs {
		if inventories[spec.importPath] == nil {
			continue
		}
		if _, err := loader.Import(spec.importPath); err != nil {
			violations = append(violations, fmt.Sprintf("%s: stable contract package does not type-check: %v", spec.importPath, err))
		}
	}
	return loader.checked, violations
}

func (i *chainedImporter) Import(path string) (*types.Package, error) {
	checked, primaryErr := i.primary.Import(path)
	if primaryErr == nil {
		return checked, nil
	}
	checked, secondaryErr := i.secondary.Import(path)
	if secondaryErr == nil {
		return checked, nil
	}
	return nil, errors.Join(primaryErr, secondaryErr)
}

func moduleExportImporter(fset *token.FileSet, root string) types.Importer {
	exportFiles := make(map[string]string)
	lookup := func(path string) (io.ReadCloser, error) {
		exportFile := exportFiles[path]
		if exportFile == "" {
			command := exec.Command("go", "list", "-export", "-f={{.Export}}", path)
			command.Dir = root
			command.Env = append(os.Environ(), "GOWORK=off")
			output, err := command.Output()
			if err != nil {
				return nil, fmt.Errorf("resolve module export for %s: %w", path, err)
			}
			exportFile = strings.TrimSpace(string(output))
			if exportFile == "" {
				return nil, fmt.Errorf("resolve module export for %s: empty export path", path)
			}
			exportFiles[path] = exportFile
		}
		return os.Open(exportFile)
	}
	return importer.ForCompiler(fset, "gc", lookup)
}

func (i *sourceImporter) Import(path string) (*types.Package, error) {
	if checked := i.checked[path]; checked != nil {
		return checked, nil
	}
	inventory := i.inventories[path]
	if inventory == nil {
		return i.fallback.Import(path)
	}
	if i.checking[path] {
		return nil, fmt.Errorf("import cycle involving %s", path)
	}
	i.checking[path] = true
	defer delete(i.checking, path)
	config := types.Config{Importer: i}
	checked, err := config.Check(path, i.fset, stableTypeCheckFiles(path, inventory.files), nil)
	if err != nil {
		return nil, err
	}
	i.checked[path] = checked
	return checked, nil
}

func stableTypeCheckFiles(packagePath string, files []*ast.File) []*ast.File {
	result := make([]*ast.File, 0, len(files))
	for _, file := range files {
		if fileDeclaresPostM9OperatorAdminSurface(file) {
			continue
		}
		implementation := false
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err == nil && strings.HasPrefix(importPath, packagePath+"/internal/") {
				implementation = true
				break
			}
		}
		if !implementation {
			result = append(result, file)
		}
	}
	return result
}

func fileDeclaresPostM9OperatorAdminSurface(file *ast.File) bool {
	exports := make(map[manifestExport]struct{})
	collectExports(file, exports)
	for exported := range exports {
		if _, participation := issue137ParticipationOperatorExports[exported]; participation {
			return true
		}
		if _, operatorAdmin := postM9OperatorAdminRuntimeExports[exported]; operatorAdmin {
			return true
		}
	}
	return false
}

func typedConstants(root string, fset *token.FileSet, pkg *types.Package) map[string]constantDeclaration {
	constants := make(map[string]constantDeclaration)
	if pkg == nil {
		return constants
	}
	for _, name := range pkg.Scope().Names() {
		object, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		typeName := ""
		if named, ok := object.Type().(*types.Named); ok && named.Obj().Pkg() == pkg {
			typeName = named.Obj().Name()
		}
		value := object.Val().ExactString()
		if object.Val().Kind() == constant.String {
			value = constant.StringVal(object.Val())
		}
		constants[name] = constantDeclaration{
			name:     name,
			rel:      relativePosition(root, fset, object.Pos()),
			typeName: typeName,
			value:    value,
		}
	}
	return constants
}

func relativePosition(root string, fset *token.FileSet, pos token.Pos) string {
	filename := fset.Position(pos).Filename
	rel, err := filepath.Rel(root, filename)
	if err != nil {
		return filepath.ToSlash(filename)
	}
	return filepath.ToSlash(rel)
}

func inspectStableType(declaration typeDeclaration) (manifestStableType, error) {
	if declaration.spec.Assign.IsValid() {
		return manifestStableType{}, errors.New("type aliases are not allowed in the stable contract")
	}
	result := manifestStableType{Name: declaration.spec.Name.Name}
	if structure, ok := declaration.spec.Type.(*ast.StructType); ok {
		result.Underlying = "struct"
		result.Fields = make([]manifestStableField, 0, len(structure.Fields.List))
		for _, field := range structure.Fields.List {
			if len(field.Names) != 1 {
				return manifestStableType{}, errors.New("stable struct fields must have exactly one explicit name")
			}
			if !ast.IsExported(field.Names[0].Name) {
				continue
			}
			typeName, err := canonicalType(field.Type, declaration.imports)
			if err != nil {
				return manifestStableType{}, err
			}
			jsonTag := ""
			if field.Tag != nil {
				jsonTag, err = strconv.Unquote(field.Tag.Value)
				if err != nil {
					return manifestStableType{}, fmt.Errorf("invalid field tag on %s: %w", field.Names[0].Name, err)
				}
			}
			result.Fields = append(result.Fields, manifestStableField{JSONTag: jsonTag, Name: field.Names[0].Name, Type: typeName})
		}
		return result, nil
	}
	underlying, err := canonicalType(declaration.spec.Type, declaration.imports)
	if err != nil {
		return manifestStableType{}, err
	}
	result.Underlying = underlying
	return result, nil
}

func canonicalType(expr ast.Expr, imports map[string]string) (string, error) {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name, nil
	case *ast.SelectorExpr:
		qualifier, ok := typed.X.(*ast.Ident)
		if !ok {
			return "", errors.New("unsupported qualified stable field type")
		}
		importPath, ok := imports[qualifier.Name]
		if !ok {
			return "", fmt.Errorf("unresolved stable field type qualifier %s", qualifier.Name)
		}
		return importPath + "." + typed.Sel.Name, nil
	case *ast.ArrayType:
		if typed.Len != nil {
			return "", errors.New("fixed arrays are not allowed in the stable contract")
		}
		element, err := canonicalType(typed.Elt, imports)
		if err != nil {
			return "", err
		}
		return "[]" + element, nil
	case *ast.StarExpr:
		element, err := canonicalType(typed.X, imports)
		if err != nil {
			return "", err
		}
		return "*" + element, nil
	case *ast.MapType:
		key, err := canonicalType(typed.Key, imports)
		if err != nil {
			return "", err
		}
		element, err := canonicalType(typed.Value, imports)
		if err != nil {
			return "", err
		}
		return "map[" + key + "]" + element, nil
	default:
		return "", fmt.Errorf("unsupported stable field type %T", expr)
	}
}

func stableTypeText(value manifestStableType) string {
	data, err := json.Marshal(value)
	if err != nil {
		return value.Name
	}
	return string(data)
}

func stableContractTypeMatches(actual, expected manifestStableType) bool {
	return reflect.DeepEqual(actual, expected)
}

func receiverName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.ParenExpr:
		return receiverName(typed.X)
	case *ast.StarExpr:
		return receiverName(typed.X)
	case *ast.IndexExpr:
		return receiverName(typed.X)
	case *ast.IndexListExpr:
		return receiverName(typed.X)
	default:
		return ""
	}
}

func safeManifestDestination(root, requested string) (string, error) {
	if !filepath.IsAbs(requested) {
		return "", fmt.Errorf("API boundary artifact destination must be an absolute path outside the repository")
	}
	requested = filepath.Clean(requested)
	if info, err := os.Lstat(requested); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("API boundary artifact destination must be outside the repository and must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("API boundary artifact destination must be a regular path outside the repository")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect API boundary artifact destination: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root for API boundary artifact: %w", err)
	}
	parent := filepath.Dir(requested)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("API boundary artifact parent must exist outside the repository: %w", err)
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", fmt.Errorf("inspect API boundary artifact parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("API boundary artifact parent must be a directory outside the repository")
	}
	resolvedDestination := filepath.Join(resolvedParent, filepath.Base(requested))
	inside, err := pathWithin(resolvedRoot, resolvedDestination)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("API boundary artifact destination must be outside the repository")
	}
	return resolvedDestination, nil
}

func writeAtomic(destination string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".api-boundary-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink destination")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return err
	}
	keep = true
	return nil
}

func gitNullList(root string, args ...string) ([]string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return splitNull(output), nil
}

func gitStagedSymlinks(root string) (map[string]string, error) {
	records, err := gitNullList(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, record := range records {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("parse git staged path record %q", record)
		}
		metadata := strings.Fields(record[:tab])
		if len(metadata) != 3 || metadata[0] != "120000" {
			continue
		}
		command := exec.Command("git", "cat-file", "blob", metadata[1])
		command.Dir = root
		target, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("inspect tracked symlink %s: %w", record[tab+1:], err)
		}
		result[filepath.ToSlash(record[tab+1:])] = string(target)
	}
	return result, nil
}

func splitNull(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func addPathOrigin(paths map[string]map[pathOrigin]struct{}, path string, origin pathOrigin) {
	if paths[path] == nil {
		paths[path] = make(map[pathOrigin]struct{})
	}
	paths[path][origin] = struct{}{}
}

func validateRelativePath(path string) error {
	if filepath.IsAbs(filepath.FromSlash(path)) {
		return fmt.Errorf("absolute paths are forbidden")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("path traversal is forbidden")
		}
	}
	return nil
}

func symlinkViolation(path, target string) string {
	targetKind := "relative"
	if filepath.IsAbs(target) {
		targetKind = "absolute"
	} else {
		for _, part := range strings.Split(filepath.ToSlash(target), "/") {
			if part == ".." {
				targetKind = "traversal"
				break
			}
		}
	}
	return fmt.Sprintf("symlink is forbidden: %s has %s target %q", path, targetKind, target)
}

func hasFoldedPathSegment(path, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

func isDocumentationPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	extension := strings.ToLower(filepath.Ext(base))
	if _, ok := documentationExtensions[extension]; ok {
		return true
	}
	if extension != "" {
		return false
	}
	name := base
	_, ok := documentationNames[name]
	return ok
}

func pathWithin(root, candidate string) (bool, error) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, fmt.Errorf("compare API boundary artifact destination with repository: %w", err)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func checkExportedName(fset *token.FileSet, rel string, ident *ast.Ident, violations *[]string) {
	if ident == nil || !ident.IsExported() {
		return
	}
	name := ident.Name
	if _, allowed := allowedOperatorAdminMutationSurface[name]; allowed {
		return
	}
	for _, fragment := range forbiddenExportFragments {
		if (name == "SHIPID" || name == "LocalSHIPID") && fragment == "SHIP" {
			continue
		}
		if name == "SourceLayerV1SpineRoundTrip" && fragment == "Spine" {
			continue
		}
		if strings.Contains(name, fragment) {
			*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes forbidden boundary term "+fragment))
			return
		}
	}
	if looksLikeMutationSurface(name) {
		*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes premature trust or pairing mutation surface"))
	}
}

func checkExportedTypeSurface(fset *token.FileSet, rel string, node ast.Node, violations *[]string) {
	ast.Inspect(node, func(current ast.Node) bool {
		ident, ok := current.(*ast.Ident)
		if ok {
			checkExportedName(fset, rel, ident, violations)
		}
		return true
	})
}

func internalImportAliases(file *ast.File, modulePath string) map[string]string {
	aliases := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasPrefix(importPath, modulePath+"/internal/") {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "" && name != "_" && name != "." {
			aliases[name] = importPath
		}
	}
	return aliases
}

func checkInternalTypeEscape(fset *token.FileSet, rel string, node ast.Node, aliases map[string]string, violations *[]string) {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		if found {
			return false
		}
		selector, ok := current.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, internal := aliases[identifier.Name]; internal {
			*violations = append(*violations, at(fset, selector.Pos(), rel, "public API packages must not import internal implementation types"))
			found = true
			return false
		}
		return true
	})
}

func checkCrossRuntimeStrings(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && strings.HasPrefix(strings.TrimSpace(value), "ebus.v1.") {
			*violations = append(*violations, at(fset, literal.Pos(), rel, "public API exposes forbidden eBUS runtime identifier"))
		}
		return true
	})
}

func looksLikeMutationSurface(name string) bool {
	for _, verb := range mutationVerbs {
		if !startsWithWord(name, verb) {
			continue
		}
		for _, noun := range mutationNouns {
			if strings.Contains(name, noun) {
				return true
			}
		}
	}
	return false
}

func startsWithWord(name string, word string) bool {
	if !strings.HasPrefix(name, word) {
		return false
	}
	if len(name) == len(word) {
		return true
	}
	next := []rune(strings.TrimPrefix(name, word))[0]
	return unicode.IsUpper(next) || unicode.IsDigit(next)
}

func hasPathSegment(path string, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func at(fset *token.FileSet, pos token.Pos, rel string, message string) string {
	position := fset.Position(pos)
	if position.IsValid() {
		return fmt.Sprintf("%s:%d: %s", rel, position.Line, message)
	}
	return rel + ": " + message
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
