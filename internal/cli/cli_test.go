package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/cli"
	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/state"
)

func TestValidateAndPlanAreSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "platoon.yaml")
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var validateOut, validateErr bytes.Buffer
	if code := cli.Run([]string{"validate", "--file", manifestPath}, &validateOut, &validateErr); code != 0 {
		t.Fatalf("validate exit = %d, stderr = %s", code, validateErr.String())
	}
	if got := validateOut.String(); got != "valid: synthetic-platoon\n" {
		t.Fatalf("validate stdout = %q", got)
	}

	var first, second, planErr bytes.Buffer
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &first, &planErr); code != 0 {
		t.Fatalf("plan exit = %d, stderr = %s", code, planErr.String())
	}
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &second, &planErr); code != 0 {
		t.Fatalf("second plan exit = %d, stderr = %s", code, planErr.String())
	}
	if first.String() != second.String() {
		t.Fatalf("plan output changed:\n%s\n%s", first.String(), second.String())
	}
	if !strings.Contains(first.String(), `"stage": "build-api"`) || !strings.Contains(first.String(), `"status": "admitted"`) {
		t.Fatalf("plan output = %s", first.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "platoon.yaml" {
		t.Fatalf("validate/plan created side effects: %#v", entries)
	}
}

func TestStartWithoutApplyPrintsPlanAndCreatesNoState(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "platoon.yaml")
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(dir, "state")
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"start", "--file", manifestPath, "--state", stateRoot}, &stdout, &stderr); code != 0 {
		t.Fatalf("start preview exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"apply": false`) || !strings.Contains(stdout.String(), `"decisions"`) {
		t.Fatalf("start preview = %s", stdout.String())
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("start preview created state: %v", err)
	}
}

func TestMutationCommandsRequireExplicitApply(t *testing.T) {
	for _, command := range []string{"drain", "resume"} {
		t.Run(command, func(t *testing.T) {
			stateRoot := filepath.Join(t.TempDir(), "state")
			var stdout, stderr bytes.Buffer
			code := cli.Run([]string{command, "--run", "run-a", "--state", stateRoot}, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "--apply is required") {
				t.Fatalf("%s exit=%d stderr=%q", command, code, stderr.String())
			}
			if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
				t.Fatalf("%s created state without apply: %v", command, err)
			}
		})
	}
}

func TestReconcilePreviewDoesNotCreateMissingState(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"reconcile", "--run", "run-a", "--state", stateRoot}, &stdout, &stderr); code != 1 {
		t.Fatalf("reconcile preview exit = %d, want 1", code)
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("reconcile preview created state: %v", err)
	}
}

func TestReconcileRejectsMaxCyclesWithoutPollBeforeOpeningState(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "preview", true: "apply"}[apply], func(t *testing.T) {
			stateRoot := filepath.Join(t.TempDir(), "state")
			args := []string{"reconcile", "--run", "run-a", "--state", stateRoot, "--max-cycles", "2"}
			if apply {
				args = append(args, "--apply")
			}
			var stdout, stderr bytes.Buffer
			code := cli.Run(args, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "--max-cycles requires --poll") {
				t.Fatalf("reconcile exit=%d stderr=%q", code, stderr.String())
			}
			if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
				t.Fatalf("invalid polling flags created state: %v", err)
			}
		})
	}
}

func TestBuildStatusReportsTokensClaimsQueuesChildrenAndCriticalReady(t *testing.T) {
	run := &state.Run{
		ID: "run-a", Status: state.RunActive, Generation: 3,
		Manifest: manifest.Manifest{Spec: manifest.Spec{
			Limits:       manifest.Limits{Implementation: 2, Review: 1},
			Repositories: []manifest.Repository{{ID: "repo", MaxWriters: 2}},
			Stages: []manifest.Stage{
				{ID: "active", Repository: "repo", Mode: manifest.Implementation, Claims: manifest.Claims{Paths: []string{"internal/api"}, Semantic: []string{"api-contract"}}},
				{ID: "ready", Repository: "repo", Mode: manifest.Implementation},
			},
		}},
		Stages: map[string]*state.StageState{
			"active": {ID: "active", Status: state.StageInProgress, FleetID: "fleet-active", Reservation: &state.Reservation{Phase: state.ReservationCommitted}},
			"ready":  {ID: "ready", Status: state.StageQueued, Blocker: "implementation token limit reached"},
		},
		MergeQueue: map[string][]*state.MergeCandidate{"repo": {{Stage: "active", Status: state.CandidateQueued}}},
		Blockers:   []state.Blocker{{Stage: "ready", Code: "capacity", Message: "token unavailable"}},
		UpdatedAt:  time.Unix(1, 0).UTC(),
	}
	report := cli.BuildStatus(run)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{`"implementationUsed":1`, `"fleetId":"fleet-active"`, `"internal/api"`, `"criticalReady":["ready"]`, `"mergeQueue"`} {
		if !strings.Contains(text, want) {
			t.Errorf("status report %s missing %s", text, want)
		}
	}
}

func TestValidateReportsUsageWithoutAFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"validate"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--file is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
