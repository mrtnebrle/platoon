package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestREADMEReconciliation(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "--initial-branch=main")
	runGit(t, repository, "config", "user.name", "Synthetic Author")
	runGit(t, repository, "config", "user.email", "author@example.invalid")
	write(t, filepath.Join(repository, "README.md"), "# Synthetic\n")
	write(t, filepath.Join(repository, "internal", "feature.go"), "package internal\n")
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "base")
	base := runGit(t, repository, "rev-parse", "HEAD")
	write(t, filepath.Join(repository, "internal", "feature.go"), "package internal\n\nconst Enabled = true\n")
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "behavior")
	head := runGit(t, repository, "rev-parse", "HEAD")
	env := testEnv(map[string]string{"PLATOON_EVENT_NAME": "push", "PLATOON_BEFORE_SHA": base, "PLATOON_HEAD_SHA": head})
	if err := run(repository, env); err == nil {
		t.Fatal("behavior change passed without README update")
	}
	write(t, filepath.Join(repository, "README.md"), "# Synthetic\n\nEnabled behavior.\n")
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "docs")
	head = runGit(t, repository, "rev-parse", "HEAD")
	env = testEnv(map[string]string{"PLATOON_EVENT_NAME": "push", "PLATOON_BEFORE_SHA": base, "PLATOON_HEAD_SHA": head})
	if err := run(repository, env); err == nil {
		t.Fatal("later README commit incorrectly satisfied earlier behavior commit")
	}
	base = head
	write(t, filepath.Join(repository, "internal", "feature.go"), "package internal\n\nconst Enabled = false\n")
	write(t, filepath.Join(repository, "README.md"), "# Synthetic\n\nDisabled behavior.\n")
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "behavior and docs")
	head = runGit(t, repository, "rev-parse", "HEAD")
	env = testEnv(map[string]string{"PLATOON_EVENT_NAME": "push", "PLATOON_BEFORE_SHA": base, "PLATOON_HEAD_SHA": head})
	if err := run(repository, env); err != nil {
		t.Fatalf("same-commit README update failed: %v", err)
	}
}

func TestOperatorFacingDocumentationRequiresREADME(t *testing.T) {
	for _, path := range []string{
		"docs/operations.md", "docs/architecture.md", "docs/threat-model.md", "docs/manifest.md", "docs/adapters.md",
	} {
		if !operatorVisible(path) {
			t.Errorf("operator documentation %q was not classified as visible", path)
		}
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", directory}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(bytes.TrimSpace(output))
}
