package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestStoreWritesValidatedStateWithRestrictivePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	lease, err := store.AcquireLease(testLeaseOptions(time.Unix(100, 0), 101, false))
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	run := testRun("run-a", lease.Generation())
	runDir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.IntentPath = filepath.Join(runDir, "intent.md")
	if _, err := store.WriteRunFile(run.ID, "intent.md", []byte("synthetic intent\n"), lease); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(run, lease); err != nil {
		t.Fatalf("SaveRun() error = %v", err)
	}
	loaded, err := store.LoadRun(run.ID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, run) {
		t.Fatalf("LoadRun() = %#v, want %#v", loaded, run)
	}

	for path, want := range map[string]os.FileMode{
		root:                                0o700,
		filepath.Join(root, "runs"):         0o700,
		filepath.Join(root, "runs", run.ID): 0o700,
		filepath.Join(root, "runs", run.ID, "state.json"): 0o600,
		filepath.Join(root, "lease.json"):                 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode %s = %o, want %o", filepath.Base(path), got, want)
		}
	}
}

func TestSupportingFilesWithoutStateAreNotAuthoritative(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(testLeaseOptions(time.Unix(100, 0), 101, false))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := store.WriteRunFile("run-a", "intent.md", []byte("synthetic intent\n"), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRun("run-a"); err == nil {
		t.Fatal("supporting intent file became run authority without state.json")
	}
	if _, err := store.WriteRunFile("run-a", "workflow.yaml", []byte("name: synthetic\n"), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRun("run-a"); err == nil {
		t.Fatal("intent and workflow became run authority without state.json")
	}
	run := testRun("run-a", lease.Generation())
	runDir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.IntentPath = filepath.Join(runDir, "intent.md")
	if err := store.SaveRun(run, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRun("run-a"); err != nil {
		t.Fatalf("final state publication was not loadable: %v", err)
	}
}

func TestLoadRunRejectsMalformedUnknownAndSymlinkState(t *testing.T) {
	tests := map[string]func(t *testing.T, statePath string){
		"truncated": func(t *testing.T, statePath string) {
			if err := os.WriteFile(statePath, []byte(`{"version":`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unknown field": func(t *testing.T, statePath string) {
			if err := os.WriteFile(statePath, []byte(`{"version":"platoon.state/v1alpha1","unknown":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"insecure mode": func(t *testing.T, statePath string) {
			raw, err := marshalJSON(testRun("run-bad", 1))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(statePath, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"invalid reservation": func(t *testing.T, statePath string) {
			run := testRun("run-bad", 1)
			run.Stages["stage"] = &StageState{
				ID: "stage", Status: StageReserved,
				Reservation: &Reservation{Phase: ReservationPhase("invented"), Generation: 1},
			}
			raw, err := marshalJSON(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"terminal journal without provenance": func(t *testing.T, statePath string) {
			run := testRun("run-bad", 1)
			run.Stages["stage"] = &StageState{
				ID: "stage", Status: StageMergeReady, FleetID: "fleet-stage",
				Result: strings.Repeat("a", 64), DagrTerminalPending: "done",
				Reservation: &Reservation{Phase: ReservationReleased, Generation: 1, FleetID: "fleet-stage"},
			}
			raw, err := marshalJSON(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"failure journal without source": func(t *testing.T, statePath string) {
			run := testRun("run-bad", 1)
			run.Stages["stage"] = &StageState{
				ID: "stage", Status: StageFailed, FleetID: "fleet-stage", DagrTerminalPending: "failed",
				Reservation: &Reservation{Phase: ReservationReleased, Generation: 1, FleetID: "fleet-stage"},
			}
			raw, err := marshalJSON(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, statePath string) {
			target := filepath.Join(filepath.Dir(statePath), "target.json")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, statePath); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			runDir := filepath.Join(store.Root(), "runs", "run-bad")
			if err := os.MkdirAll(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			setup(t, filepath.Join(runDir, "state.json"))
			if _, err := store.LoadRun("run-bad"); err == nil {
				t.Fatal("LoadRun() succeeded for untrusted state")
			}
		})
	}
}

func TestLeaseBlocksConcurrentAndAmbiguousOwners(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0)
	first, err := store.AcquireLease(testLeaseOptions(now, 201, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Release() })

	if _, err := store.AcquireLease(testLeaseOptions(now.Add(time.Hour), 202, false)); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("concurrent AcquireLease() error = %v, want ErrLeaseHeld", err)
	}

	first.crash()
	options := testLeaseOptions(now.Add(2*time.Minute), 202, false)
	options.Hostname = "different-host"
	if _, err := store.AcquireLease(options); !errors.Is(err, ErrLeaseAmbiguous) {
		t.Fatalf("cross-host recovery error = %v, want ErrLeaseAmbiguous", err)
	}
}

func TestLeaseRecoveryRequiresExpiryAndProvenProcessAbsence(t *testing.T) {
	now := time.Unix(300, 0)
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcquireLease(testLeaseOptions(now, 301, false))
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() != 1 {
		t.Fatalf("generation = %d, want 1", first.Generation())
	}
	first.crash()

	if _, err := store.AcquireLease(testLeaseOptions(now.Add(30*time.Second), 302, false)); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("early recovery error = %v, want ErrLeaseHeld", err)
	}

	live := testLeaseOptions(now.Add(2*time.Minute), 302, true)
	if _, err := store.AcquireLease(live); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("live recovery error = %v, want ErrLeaseHeld", err)
	}

	recovered, err := store.AcquireLease(testLeaseOptions(now.Add(2*time.Minute), 302, false))
	if err != nil {
		t.Fatalf("stale recovery error = %v", err)
	}
	t.Cleanup(func() { _ = recovered.Release() })
	if recovered.Generation() != 2 {
		t.Fatalf("recovered generation = %d, want 2", recovered.Generation())
	}

	if err := store.SaveRun(testRun("fenced", first.Generation()), first); !errors.Is(err, ErrFenced) {
		t.Fatalf("old-generation SaveRun() error = %v, want ErrFenced", err)
	}
}

func TestOpenRejectsInsecureOrSymlinkRoots(t *testing.T) {
	parent := t.TempDir()
	insecure := filepath.Join(parent, "insecure")
	if err := os.Mkdir(insecure, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := Open(insecure); err == nil {
			t.Fatal("Open() accepted group/world-accessible root")
		}
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("Open() accepted symlink root")
	}
}

func TestRunValidateRejectsConflictingTerminalChildIdentity(t *testing.T) {
	run := testRun("run-a", 1)
	run.Stages["stage"] = &StageState{
		ID: "stage", Status: StageFailed, FleetID: "fleet-a", DagrTerminalPending: "failed", FailureSource: "child",
		Worktree: "worktree", WorktreeGitPointer: "gitdir: git-dir", WorktreeGitDir: "git-dir", WorktreeIdentity: "1:1", GitDirIdentity: "1:2", InitialSHA: strings.Repeat("a", 40),
		Reservation: &Reservation{
			Phase: ReservationReleased, Generation: 1, FleetID: "fleet-b", CorrelationID: "run-a-stage",
		},
	}
	if err := run.Validate(); err == nil || !strings.Contains(err.Error(), "child identity") {
		t.Fatalf("Validate() error = %v, want child identity conflict", err)
	}
}

func TestRunValidateRejectsForgedAdoptionProvenance(t *testing.T) {
	run := testRun("run-a", 1)
	run.Stages["stage"] = &StageState{
		ID: "stage", Status: StageFailed, FleetID: "fleet-stage", Adopted: true,
		Worktree: "worktree", WorktreeGitPointer: "gitdir: git-dir", WorktreeGitDir: "git-dir", WorktreeIdentity: "1:1", GitDirIdentity: "1:2", InitialSHA: strings.Repeat("a", 40),
		DagrTerminalPending: "failed", FailureSource: "child",
		Reservation: &Reservation{Phase: ReservationReleased, Generation: 1, FleetID: "fleet-stage"},
	}
	if err := run.Validate(); err == nil || !strings.Contains(err.Error(), "adopted state") {
		t.Fatalf("Validate() error = %v, want forged adoption rejection", err)
	}
}

func testLeaseOptions(now time.Time, pid int, alive bool) LeaseOptions {
	return LeaseOptions{
		TTL:        time.Minute,
		Now:        func() time.Time { return now },
		Hostname:   "host-a",
		PID:        pid,
		OwnerAlive: func(host string, candidate int) (bool, error) { return alive, nil },
	}
}

func testRun(id string, generation uint64) *Run {
	m := manifest.Manifest{
		APIVersion: manifest.APIVersion,
		Kind:       manifest.Kind,
		Metadata:   manifest.Metadata{Name: "synthetic-platoon"},
		Spec: manifest.Spec{
			Project: "synthetic-project", Mission: "docs/mission.md", Intent: "docs/intent.md",
			Limits: manifest.Limits{Implementation: 1, Review: 1, LeaseTTL: "1m", CommandTimeout: "1m", MaxOutputBytes: 4096},
			Adapters: manifest.Adapters{
				Dagr: manifest.DagrAdapter{Executable: "dagr", Database: "dagr.db", InspectExecutable: "sqlite3"},
				Sergeant: manifest.SergeantAdapter{
					FleetRoot: "fleets", OriginProfile: "platoon-local",
					Dispatch: manifest.Command{Executable: "sgt-dispatch"}, Watch: manifest.Command{Executable: "sgt-watch"},
					Wake: manifest.Command{Executable: "sgt-wake"}, Drain: manifest.Command{Executable: "sgt-drain"},
				},
			},
			Routing:      []manifest.Route{{Model: "reasoning", Risk: "high", Harness: "opencode"}},
			Repositories: []manifest.Repository{{ID: "repo", Path: "repos/repo", Branch: "feat/test", MaxWriters: 1, Integration: []manifest.Command{}}},
			Stages: []manifest.Stage{{
				ID: "stage", Repository: "repo", Task: "task-stage", Mode: manifest.Implementation,
				Harness: "opencode", Model: "reasoning", Risk: "high", DependsOn: []string{},
				Claims:     manifest.Claims{Paths: []string{"internal/stage"}, Semantic: []string{"stage-contract"}},
				Acceptance: []manifest.Command{{Executable: "go", Args: []string{"test", "./..."}}},
			}},
		},
	}
	manifestDigest, _ := digestManifest(m)
	intentDigest := sha256.Sum256([]byte("synthetic intent\n"))
	return &Run{
		Version: StateVersion, ID: id, Name: "synthetic-platoon",
		ManifestDigest: manifestDigest, ManifestSourceDigest: strings.Repeat("b", 64),
		ManifestPath: "manifest.yaml", IntentPath: "intent.md", IntentRevision: hex.EncodeToString(intentDigest[:]),
		Manifest: m, Generation: generation, Status: RunActive,
		Dagr:       DagrState{Workflow: "workflow", RunID: "dagr-run", Stages: map[string]string{"stage": "dagr-stage"}, Phase: "active"},
		Stages:     map[string]*StageState{"stage": {ID: "stage", Status: StagePending}},
		MergeQueue: map[string][]*MergeCandidate{},
	}
}
