package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFleetReaderVerifiesBindingAndTerminalEvidenceWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fleets")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 64)
	repoDir := writeFleet(t, root, "synthetic-fleet-a", "synthetic-api", worktree, revision)
	if err := os.WriteFile(filepath.Join(repoDir, "status"), []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "result"), []byte("https://example.invalid/change/1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(repoDir, "status"))
	if err != nil {
		t.Fatal(err)
	}

	reader := NewFleetReader(root)
	evidence, err := reader.Read("synthetic-fleet-a", "synthetic-api", FleetBinding{
		Project: "synthetic-project", Task: "task-build-api", Stage: "build-api", Branch: "feat/synthetic-build-api", IntentRevision: revision,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if evidence.Status != FleetDone || evidence.ResultDigest == "" || evidence.Worktree != worktree {
		t.Fatalf("Read() = %#v", evidence)
	}
	after, err := os.ReadFile(filepath.Join(repoDir, "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Read() mutated child status: %q -> %q", before, after)
	}
}

func TestFleetReaderFailsClosedOnMismatchedOrSymlinkEvidence(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, repoDir string){
		"project mismatch": func(t *testing.T, repoDir string) {
			writeOwned(t, filepath.Join(repoDir, "project"), "different-project\n")
		},
		"branch mismatch": func(t *testing.T, repoDir string) {
			writeOwned(t, filepath.Join(repoDir, "branch"), "feat/different-branch\n")
		},
		"missing result": func(t *testing.T, repoDir string) {
			writeOwned(t, filepath.Join(repoDir, "status"), "done\n")
		},
		"unknown status": func(t *testing.T, repoDir string) {
			writeOwned(t, filepath.Join(repoDir, "status"), "complete-ish\n")
		},
		"symlink status": func(t *testing.T, repoDir string) {
			if err := os.Remove(filepath.Join(repoDir, "status")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("project", filepath.Join(repoDir, "status")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fleets")
			worktree := filepath.Join(t.TempDir(), "worktree")
			if err := os.MkdirAll(worktree, 0o700); err != nil {
				t.Fatal(err)
			}
			revision := strings.Repeat("b", 64)
			repoDir := writeFleet(t, root, "synthetic-fleet-a", "synthetic-api", worktree, revision)
			mutate(t, repoDir)
			_, err := NewFleetReader(root).Read("synthetic-fleet-a", "synthetic-api", FleetBinding{
				Project: "synthetic-project", Task: "task-build-api", Stage: "build-api", Branch: "feat/synthetic-build-api", IntentRevision: revision,
			})
			if err == nil {
				t.Fatal("Read() accepted unverified fleet evidence")
			}
		})
	}
}

func TestFleetReaderRejectsMultiRepositoryFleet(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fleets")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("b", 64)
	writeFleet(t, root, "synthetic-fleet-a", "synthetic-api", worktree, revision)
	if err := os.MkdirAll(filepath.Join(root, "synthetic-fleet-a", "synthetic-docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := NewFleetReader(root).Read("synthetic-fleet-a", "synthetic-api", FleetBinding{
		Project: "synthetic-project", Task: "task-build-api", Stage: "build-api",
		Branch: "feat/synthetic-build-api", IntentRevision: revision,
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one repository") {
		t.Fatalf("Read() error = %v, want one-repository rejection", err)
	}
}

func TestFleetReaderRequiresDispatchCorrelationWhenRequested(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fleets")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("b", 64)
	writeFleet(t, root, "synthetic-fleet-a", "synthetic-api", worktree, revision)
	callbackDir := filepath.Join(root, "synthetic-fleet-a", ".callbacks")
	if err := os.MkdirAll(callbackDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeOwned(t, filepath.Join(callbackDir, "origin.json"), `{"version":"sergeant.callback-origin/v1","profile":"platoon-local","correlation_id":"different-correlation"}`+"\n")
	_, err := NewFleetReader(root).Read("synthetic-fleet-a", "synthetic-api", FleetBinding{
		Project: "synthetic-project", Task: "task-build-api", Stage: "build-api",
		Branch: "feat/synthetic-build-api", IntentRevision: revision,
		RequireCorrelation: true, OriginProfile: "platoon-local", CorrelationID: "run-a-build-api",
	})
	if err == nil || !strings.Contains(err.Error(), "correlation") {
		t.Fatalf("Read() error = %v, want correlation rejection", err)
	}
}

func TestFleetReaderFindsExactlyCorrelatedFleets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fleets")
	for _, fleet := range []string{"synthetic-fleet-b", "synthetic-fleet-a"} {
		callbackDir := filepath.Join(root, fleet, ".callbacks")
		if err := os.MkdirAll(callbackDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeOwned(t, filepath.Join(callbackDir, "origin.json"), `{"version":"sergeant.callback-origin/v1","profile":"platoon-local","correlation_id":"run-a-build-api"}`+"\n")
	}
	callbackDir := filepath.Join(root, "unrelated-fleet", ".callbacks")
	if err := os.MkdirAll(callbackDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeOwned(t, filepath.Join(callbackDir, "origin.json"), `{"version":"sergeant.callback-origin/v1","profile":"other","correlation_id":"run-a-build-api"}`+"\n")

	got, err := NewFleetReader(root).FindByCorrelation("platoon-local", "run-a-build-api")
	if err != nil {
		t.Fatalf("FindByCorrelation() error = %v", err)
	}
	want := []string{"synthetic-fleet-a", "synthetic-fleet-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindByCorrelation() = %#v, want %#v", got, want)
	}
}

func TestGitInspectorAccountsForTrackedRenamedDeletedUntrackedAndSymlinkPaths(t *testing.T) {
	repository := t.TempDir()
	runGit(t, repository, "init", "--initial-branch=main")
	runGit(t, repository, "config", "user.name", "Synthetic Author")
	runGit(t, repository, "config", "user.email", "author@example.invalid")
	writeFile(t, filepath.Join(repository, "internal", "api", "existing.go"), "package api\n")
	writeFile(t, filepath.Join(repository, "internal", "old", "move.go"), "package old\n")
	writeFile(t, filepath.Join(repository, "docs", "delete.md"), "delete me\n")
	writeFile(t, filepath.Join(repository, ".gitignore"), "ignored.tmp\n")
	if err := os.Symlink("delete.md", filepath.Join(repository, "docs", "old-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("delete.md", filepath.Join(repository, "docs", "replaced-link")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(repository, "internal", "api", "existing.go"), "package api\n// changed\n")
	if err := os.Rename(filepath.Join(repository, "internal", "old", "move.go"), filepath.Join(repository, "outside.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, "docs", "delete.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, "docs", "old-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, "docs", "replaced-link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repository, "docs", "replaced-link"), "regular now\n")
	writeFile(t, filepath.Join(repository, "ignored.tmp"), "must still be inspected\n")
	writeFile(t, filepath.Join(repository, "internal", "api", "new.go"), "package api\n")
	writeFile(t, filepath.Join(repository, ".sergeant-status"), "done\n")
	if err := os.Symlink("../outside.go", filepath.Join(repository, "internal", "api", "link.go")); err != nil {
		t.Fatal(err)
	}

	inspector := NewGitInspector(OSExecutor{}, time.Minute, 64<<10)
	changed, err := inspector.ChangedPaths(context.Background(), repository, base)
	if err != nil {
		t.Fatalf("ChangedPaths() error = %v", err)
	}
	got := make([]string, 0, len(changed))
	for _, item := range changed {
		got = append(got, item.Path)
	}
	sort.Strings(got)
	want := []string{"docs/delete.md", "docs/old-link", "docs/replaced-link", "ignored.tmp", "internal/api/existing.go", "internal/api/link.go", "internal/api/new.go", "internal/old/move.go", "outside.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPaths() = %#v, want %#v", got, want)
	}
	changedByPath := map[string]ChangedPath{}
	for _, item := range changed {
		changedByPath[item.Path] = item
	}
	for _, oldSymlink := range []string{"docs/old-link", "docs/replaced-link"} {
		if !changedByPath[oldSymlink].Symlink {
			t.Errorf("historical symlink %q was not identified", oldSymlink)
		}
	}

	violations := CheckPathClaims(changed, []string{"internal/api"})
	violationPaths := make([]string, 0, len(violations))
	for _, violation := range violations {
		violationPaths = append(violationPaths, violation.Path)
		if strings.ContainsAny(violation.Path, "\r\n\x1b") {
			t.Fatalf("diagnostic path was not sanitized: %q", violation.Path)
		}
	}
	sort.Strings(violationPaths)
	wantViolations := []string{"docs/delete.md", "docs/old-link", "docs/replaced-link", "ignored.tmp", "internal/api/link.go", "internal/old/move.go", "outside.go"}
	if !reflect.DeepEqual(violationPaths, wantViolations) {
		t.Fatalf("violations = %#v, want %#v", violationPaths, wantViolations)
	}
}

func writeFleet(t *testing.T, root, fleet, repo, worktree, revision string) string {
	t.Helper()
	repoDir := filepath.Join(root, fleet, repo)
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeOwned(t, filepath.Join(root, fleet, "intent_revision"), revision+"\n")
	writeOwned(t, filepath.Join(repoDir, "intent_revision"), revision+"\n")
	writeOwned(t, filepath.Join(repoDir, "project"), "synthetic-project\n")
	writeOwned(t, filepath.Join(repoDir, "td_task"), "task-build-api\n")
	writeOwned(t, filepath.Join(repoDir, "stage"), "build-api\n")
	writeOwned(t, filepath.Join(repoDir, "branch"), "feat/synthetic-build-api\n")
	writeOwned(t, filepath.Join(repoDir, "status"), "in_progress\n")
	writeOwned(t, filepath.Join(repoDir, "worktree"), worktree+"\n")
	writeOwned(t, filepath.Join(repoDir, "initial_sha"), strings.Repeat("c", 40)+"\n")
	return repoDir
}

func writeOwned(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, value string) {
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
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
