package compliance_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRequiredPublicDocumentationExists(t *testing.T) {
	for _, file := range []string{
		"README.md",
		"LICENSE",
		"CONTRIBUTING.md",
		"AGENTS.md",
		"schema/platoon.schema.json",
		"examples/platoon.yaml",
		"docs/architecture.md",
		"docs/threat-model.md",
		"docs/manifest.md",
		"docs/adapters.md",
		"docs/operations.md",
		".github/workflows/ci.yml",
	} {
		info, err := os.Stat(filepath.Join("../..", filepath.FromSlash(file)))
		if err != nil || info.IsDir() {
			t.Errorf("required public artifact %q is missing", file)
		}
	}
}

func TestREADMETracksOperatorVisibleContracts(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(raw)
	for _, command := range []string{"validate", "plan", "start", "reconcile", "status", "drain", "resume"} {
		if !strings.Contains(readme, "`"+command+" ") && !strings.Contains(readme, "platoon "+command+" ") {
			t.Errorf("README does not document command %q", command)
		}
	}
	for _, field := range []string{
		"apiVersion", "project", "mission", "intent", "implementation", "review",
		"leaseTTL", "commandTimeout", "maxOutputBytes", "inspectExecutable", "fleetRoot", "originProfile",
		"maxWriters", "dependsOn", "claims.paths", "claims.semantic", "acceptance", "adoptFleet",
	} {
		if !strings.Contains(readme, field) && !fileContains(t, "../../docs/manifest.md", field) {
			t.Errorf("operator docs do not mention manifest field %q", field)
		}
	}
}

func TestPublicTreeContainsNoLocalOrInternalEvidence(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	internalTask := regexp.MustCompile(`\btd-[0-9a-f]{6,}\b`)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() && (name == ".git" || name == ".platoon" || name == "graphify-out" || strings.HasPrefix(name, ".sergeant-")) {
			return filepath.SkipDir
		}
		if entry.IsDir() || strings.HasPrefix(name, ".sergeant-") || !publicTextFile(name) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range [][]byte{
			[]byte("/" + "Users/"),
			[]byte("/" + "home/"),
			{0x1b},
		} {
			if bytes.Contains(raw, forbidden) {
				t.Errorf("%s contains local or terminal evidence", relative(root, path))
			}
		}
		if internalTask.Match(raw) {
			t.Errorf("%s contains a non-synthetic task identifier", relative(root, path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publicTextFile(name string) bool {
	if name == "Makefile" || name == "LICENSE" || name == ".gitignore" || name == "go.mod" || name == "go.sum" {
		return true
	}
	switch filepath.Ext(name) {
	case ".go", ".md", ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func fileContains(t *testing.T, path, value string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(raw), value)
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return value
}
