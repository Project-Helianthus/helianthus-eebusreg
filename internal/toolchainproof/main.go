package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	goversion "go/version"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

type config struct {
	repoRoot      string
	maxGo         string
	activeGo      string
	modulePath    string
	moduleVersion string
	moduleJSON    string
}

type moduleInfo struct {
	Path    string
	Version string
	Replace *struct {
		Path    string
		Version string
	}
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.repoRoot, "repo-root", ".", "repository root")
	flag.StringVar(&cfg.maxGo, "max-go", "", "maximum supported Go toolchain version")
	flag.StringVar(&cfg.activeGo, "active-go", "", "active go env GOVERSION value")
	flag.StringVar(&cfg.modulePath, "module", "github.com/Project-Helianthus/helianthus-eebus-go", "module path to verify")
	flag.StringVar(&cfg.moduleVersion, "version", "v0.7.1-helianthus.2", "module version to verify")
	flag.StringVar(&cfg.moduleJSON, "module-json", "", "optional go list -m -json file")
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	if cfg.repoRoot == "" {
		return errors.New("repo root is required")
	}
	if cfg.maxGo == "" {
		if cfg.activeGo == "" {
			return errors.New("max-go or active-go is required")
		}
		cfg.maxGo = cfg.activeGo
	}
	goModPath := filepath.Join(cfg.repoRoot, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	file, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return fmt.Errorf("parse go.mod: %w", err)
	}
	if file.Module == nil || file.Module.Mod.Path != "github.com/Project-Helianthus/helianthus-eebusreg" {
		return fmt.Errorf("unexpected module path %q", modulePath(file))
	}
	if file.Go == nil {
		return errors.New("go.mod must declare a go directive")
	}
	if cfg.activeGo != "" {
		if err := requireVersionAtMost("active Go binary", cfg.activeGo, cfg.maxGo); err != nil {
			return err
		}
	}
	if err := requireVersionAtMost("go directive", file.Go.Version, cfg.maxGo); err != nil {
		return err
	}
	if file.Toolchain != nil {
		toolchainVersion := strings.TrimPrefix(file.Toolchain.Name, "go")
		if err := requireVersionAtMost("toolchain directive", toolchainVersion, cfg.maxGo); err != nil {
			return err
		}
	}
	if err := verifyReplacements(file, cfg.modulePath); err != nil {
		return err
	}
	if err := verifyRequiredModule(file, cfg.modulePath, cfg.moduleVersion); err != nil {
		return err
	}
	if cfg.moduleJSON != "" {
		if err := verifyModuleJSON(cfg.moduleJSON, cfg.modulePath, cfg.moduleVersion); err != nil {
			return err
		}
	}
	fmt.Printf("toolchain boundary ok: active=%s go=%s max=%s module=%s@%s\n", cfg.activeGo, file.Go.Version, cfg.maxGo, cfg.modulePath, cfg.moduleVersion)
	return nil
}

func modulePath(file *modfile.File) string {
	if file == nil || file.Module == nil {
		return ""
	}
	return file.Module.Mod.Path
}

func verifyReplacements(file *modfile.File, protectedModule string) error {
	for _, replacement := range file.Replace {
		return fmt.Errorf("replace directives are not allowed: %s => %s", replacement.Old.Path, replacement.New.Path)
	}
	return nil
}

func verifyRequiredModule(file *modfile.File, modulePath string, version string) error {
	for _, req := range file.Require {
		if req.Mod.Path != modulePath {
			continue
		}
		if req.Mod.Version != version {
			return fmt.Errorf("%s version = %s, want %s", modulePath, req.Mod.Version, version)
		}
		return nil
	}
	return fmt.Errorf("%s requirement missing", modulePath)
}

func verifyModuleJSON(path string, modulePath string, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read module json: %w", err)
	}
	var module moduleInfo
	if err := json.Unmarshal(data, &module); err != nil {
		return fmt.Errorf("parse module json: %w", err)
	}
	if module.Path != modulePath {
		return fmt.Errorf("module json path = %s, want %s", module.Path, modulePath)
	}
	if module.Version != version {
		return fmt.Errorf("module json version = %s, want %s", module.Version, version)
	}
	if module.Replace != nil {
		return fmt.Errorf("module json has replacement: %+v", module.Replace)
	}
	return nil
}

func requireVersionAtMost(label string, got string, max string) error {
	gotVersion, err := normalizeGoVersion(got)
	if err != nil {
		return fmt.Errorf("%s version %q: %w", label, got, err)
	}
	maxVersion, err := normalizeGoVersion(max)
	if err != nil {
		return fmt.Errorf("max Go version %q: %w", max, err)
	}
	if goversion.Compare(goversion.Lang(gotVersion), goversion.Lang(maxVersion)) > 0 {
		return fmt.Errorf("%s version %s exceeds max %s", label, got, max)
	}
	return nil
}

func normalizeGoVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty Go version")
	}
	if !strings.HasPrefix(value, "go") {
		value = "go" + value
	}
	if !goversion.IsValid(value) {
		return "", errors.New("invalid Go toolchain version")
	}
	return value, nil
}
