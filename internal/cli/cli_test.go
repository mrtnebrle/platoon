package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestTypedMissionPreviewMatchesValidatePlanAndStartWithoutSideEffects(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTypedMissionFixture(t, dir, validTypedMission)

	var validateOut, validateErr bytes.Buffer
	if code := cli.Run([]string{"validate", "--file", manifestPath}, &validateOut, &validateErr); code != 0 {
		t.Fatalf("validate exit = %d, stderr = %s", code, validateErr.String())
	}
	for _, want := range []string{
		"mission: mode=declaration-v1alpha1 schema=platoon.dev/mission/v1alpha1 class=deliver ready=true",
		"outputs: acceptance-evidence, product-delta",
		"sufficiency: ready: all required outputs are declared",
	} {
		if !strings.Contains(validateOut.String(), want) {
			t.Fatalf("validate output %q missing %q", validateOut.String(), want)
		}
	}

	var planOut, planErr bytes.Buffer
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &planOut, &planErr); code != 0 {
		t.Fatalf("plan exit = %d, stderr = %s", code, planErr.String())
	}
	var plan struct {
		Mission struct {
			Mode        string   `json:"mode"`
			Schema      string   `json:"schema"`
			Class       string   `json:"class"`
			Ready       bool     `json:"ready"`
			Outputs     []string `json:"outputs"`
			Sufficiency []struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"sufficiency"`
		} `json:"mission"`
	}
	if err := json.Unmarshal(planOut.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, planOut.String())
	}
	if plan.Mission.Mode != "declaration-v1alpha1" || plan.Mission.Schema != "platoon.dev/mission/v1alpha1" ||
		plan.Mission.Class != "deliver" || !plan.Mission.Ready ||
		strings.Join(plan.Mission.Outputs, ",") != "acceptance-evidence,product-delta" ||
		len(plan.Mission.Sufficiency) != 1 || plan.Mission.Sufficiency[0].Status != "ready" {
		t.Fatalf("typed plan mission = %#v", plan.Mission)
	}

	var startOut, startErr bytes.Buffer
	stateRoot := filepath.Join(dir, "state")
	if code := cli.Run([]string{"start", "--file", manifestPath, "--state", stateRoot}, &startOut, &startErr); code != 0 {
		t.Fatalf("start exit = %d, stderr = %s", code, startErr.String())
	}
	if !bytes.Contains(startOut.Bytes(), []byte(`"mission"`)) || !bytes.Contains(startOut.Bytes(), []byte(`"ready": true`)) {
		t.Fatalf("start preview = %s", startOut.String())
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("typed previews created state: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("typed previews created side effects: %#v", entries)
	}
}

func TestSurveyProducesBoundedBundleWithoutState(t *testing.T) {
	declaration := strings.Replace(validTypedMission,
		"allowed: [dagr-load-workflow", "allowed: [read-source, dagr-load-workflow", 1)
	declaration = strings.Replace(declaration,
		"    callers:\n", "    callers:\n      read-source: [operator]\n", 1)
	declaration = strings.Replace(declaration,
		"effects: [write-claimed-source]\n      claim: source-is-authoritative",
		"effects: [read-source, write-claimed-source]\n      claim: source-is-authoritative", 1)
	declaration = strings.Replace(declaration, "  unknowns: []", `    - id: operator-read
      source: mission-policy
      effects: [read-source]
      claim: actor-may-attempt
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
      actorRole: operator
  unknowns: []`, 1)
	dir := t.TempDir()
	manifestPath := writeTypedMissionFixture(t, dir, declaration)
	stateRoot := filepath.Join(dir, "state")
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"survey", "--file", manifestPath, "--caller", "operator"}, &stdout, &stderr); code != 0 {
		t.Fatalf("survey exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema": "platoon.source-bundle/v1alpha1"`) ||
		!strings.Contains(stdout.String(), `"quality": "unsupported"`) {
		t.Fatalf("survey output=%s", stdout.String())
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("survey created state: %v", err)
	}
}

func TestSurveyAndBundleFlagsPreserveReferenceModeWithoutState(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "platoon.yaml")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(dir, "state")
	for _, args := range [][]string{
		{"survey", "--file", manifestPath, "--caller", "operator"},
		{"plan", "--file", manifestPath, "--source-bundle", filepath.Join(dir, "missing.json")},
	} {
		var stdout, stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "unsupported") {
			t.Fatalf("args=%v exit=%d stderr=%q", args, code, stderr.String())
		}
		if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
			t.Fatalf("args=%v created state: %v", args, err)
		}
	}
}

func TestTypedMissionFailuresPrecedeStateAndAdapters(t *testing.T) {
	tests := map[string]struct {
		rewrite func(string) string
		prepare func(*testing.T, string)
		want    string
	}{
		"missing file": {
			prepare: func(t *testing.T, dir string) { t.Helper(); os.Remove(filepath.Join(dir, "mission.yaml")) },
			want:    "reason=missing",
		},
		"symlink": {
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				missionPath := filepath.Join(dir, "mission.yaml")
				if err := os.Remove(missionPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target.yaml", missionPath); err != nil {
					t.Fatal(err)
				}
			},
			want: "reason=not-regular",
		},
		"oversized": {
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "mission.yaml"), bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "reason=oversized",
		},
		"unknown schema": {
			rewrite: func(s string) string {
				return strings.Replace(s, "platoon.dev/mission/v1alpha1", "platoon.dev/mission/v2", 1)
			},
			want: "reason=unknown-schema",
		},
		"unknown field": {
			rewrite: func(s string) string { return strings.Replace(s, "  objective:", "  extra: value\n  objective:", 1) },
			want:    "reason=unknown-field",
		},
		"scalar coercion": {
			rewrite: func(s string) string {
				return strings.Replace(s, "objective: Deliver a synthetic API and its public guide.", "objective: 123", 1)
			},
			want: "reason=invalid-schema",
		},
		"missing unattended boolean": {
			rewrite: func(s string) string { return strings.Replace(s, "    requested: false\n", "", 1) },
			want:    "reason=invalid-schema",
		},
		"missing unknown boolean": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  unknowns: []", `  unknowns:
    - id: release-window
      question: Is entry allowed?
      attemptedSources: [mission-policy]
      route: mission-policy`, 1)
			},
			want: "reason=invalid-schema",
		},
		"unknown effect": {
			rewrite: func(s string) string { return strings.Replace(s, "dagr-load-workflow", "teleport", 1) },
			want:    "reason=unknown-effect",
		},
		"effect outside class ceiling": {
			rewrite: func(s string) string {
				return strings.Replace(s, "dagr-load-workflow", "receiving-system-operation", 1)
			},
			want: "reason=effect-class-ceiling",
		},
		"unknown output": {
			rewrite: func(s string) string { return strings.Replace(s, "category: product-delta", "category: surprise", 1) },
			want:    "reason=unknown-output",
		},
		"source schema mismatch": {
			rewrite: func(s string) string { return strings.Replace(s, "platoon.policy/v1alpha1", "git.object/v1", 1) },
			want:    "reason=invalid-source",
		},
		"malformed stop": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: unsafe-entry
      predicate:
        source: mission-policy
        field: quality
        operator: quality_is
        value: verified
      scope:
        entry: true
        stages: []
        effects: []`, 1)
			},
			want: "reason=malformed-stop",
		},
		"unknown stop stage": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: unsafe-entry
      predicate:
        source: mission-policy
        field: quality
        operator: quality_is
        value: verified
      scope:
        entry: false
        stages: [missing-stage]
        effects: []
      route: mission-policy`, 1)
			},
			want: "reason=unknown-stop-stage",
		},
		"unknown stop field": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: unsafe-entry
      predicate:
        source: mission-policy
        field: arbitraryPrivateField
        operator: exists
      scope:
        entry: true
        stages: []
        effects: []
      route: mission-policy`, 1)
			},
			want: "reason=malformed-stop",
		},
		"stop operator field mismatch": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: revision-quality
      predicate:
        source: mission-policy
        field: revision
        operator: quality_is
        value: verified
      scope:
        entry: false
        stages: [build-api]
        effects: []
      route: mission-policy`, 1)
			},
			want: "reason=malformed-stop",
		},
		"stop scalar type mismatch": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: revision-object
      predicate:
        source: mission-policy
        field: revision
        operator: equals
        value: {nested: value}
      scope:
        entry: false
        stages: [build-api]
        effects: []
      route: mission-policy`, 1)
			},
			want: "reason=malformed-stop",
		},
		"stop mixed list": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  stops: []", `  stops:
    - id: revision-list
      predicate:
        source: mission-policy
        field: revision
        operator: in
        value: [v1, true]
      scope:
        entry: false
        stages: [build-api]
        effects: []
      route: mission-policy`, 1)
			},
			want: "reason=malformed-stop",
		},
		"malformed authority tuple": {
			rewrite: func(s string) string {
				return strings.Replace(s, "actorRole: platoon", "actorRole: nobody", 1)
			},
			want: "reason=malformed-authority",
		},
		"partially unmatched actor assumption": {
			rewrite: func(s string) string {
				return strings.Replace(s,
					"effects: [dagr-load-workflow, dagr-start-run, dagr-ack-stage, sergeant-dispatch, run-validation]\n      claim: actor-may-attempt",
					"effects: [dagr-load-workflow, dagr-start-run, dagr-ack-stage, sergeant-dispatch, run-validation, write-claimed-source]\n      claim: actor-may-attempt", 1)
			},
			want: "reason=malformed-authority",
		},
		"extra wrong-kind source authority": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  unknowns: []", `    - id: wrong-dagr-authority
      source: mission-policy
      effects: [dagr-start-run]
      claim: source-is-authoritative
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
  unknowns: []`, 1)
			},
			want: "reason=malformed-authority",
		},
		"disposition owner with effects": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  unknowns: []", `    - id: release-owner
      source: mission-policy
      effects: [dagr-start-run]
      claim: owner-may-disposition
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
  unknowns:
    - id: release-window
      question: Is entry allowed?
      blocking: true
      attemptedSources: [mission-policy]
      route: mission-policy`, 1)
			},
			want: "reason=malformed-authority",
		},
		"missing authority effects": {
			rewrite: func(s string) string {
				return strings.Replace(s, "      effects: [dagr-load-workflow, dagr-start-run, dagr-ack-stage]\n", "", 1)
			},
			want: "reason=malformed-authority",
		},
		"wrong authority source kind": {
			rewrite: func(s string) string {
				return strings.Replace(s, "source: dagr-authority", "source: mission-policy", 1)
			},
			want: "reason=malformed-authority",
		},
		"write effect on review stage": {
			rewrite: func(s string) string {
				return strings.Replace(s, "review-release: [sergeant-dispatch, run-validation]", "review-release: [sergeant-dispatch, run-validation, write-claimed-source]", 1)
			},
			want: "reason=invalid-declaration",
		},
		"stage caller without assignment": {
			rewrite: func(s string) string {
				s = strings.Replace(s, "build-api: [sergeant-dispatch, run-validation, write-claimed-source]", "build-api: [sergeant-dispatch, run-validation]", 1)
				s = strings.Replace(s, "write-guide: [sergeant-dispatch, run-validation, write-claimed-source]", "write-guide: [sergeant-dispatch, run-validation]", 1)
				return s
			},
			want: "reason=invalid-declaration",
		},
		"abbreviated git revision": {
			rewrite: func(s string) string {
				return strings.ReplaceAll(s, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "v1")
			},
			want: "reason=invalid-source",
		},
		"unrouted contradiction": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  contradictions: []", `  contradictions:
    - id: conflicting-policy
      sources: [mission-policy, mission-policy]
      decision: entry`, 1)
			},
			want: "reason=unrouted-contradiction",
		},
		"unowned unknown route": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  unknowns: []", `  unknowns:
    - id: release-window
      question: Is entry allowed?
      blocking: true
      attemptedSources: [mission-policy]
      route: mission-policy`, 1)
			},
			want: "reason=malformed-authority",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			declaration := validTypedMission
			if tc.rewrite != nil {
				declaration = tc.rewrite(declaration)
			}
			manifestPath := writeTypedMissionFixture(t, dir, declaration)
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			stateRoot := filepath.Join(dir, "state")
			var stdout, stderr bytes.Buffer
			code := cli.Run([]string{"start", "--file", manifestPath, "--state", stateRoot, "--apply"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("start exit=%d stderr=%q, want %q", code, stderr.String(), tc.want)
			}
			if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
				t.Fatalf("invalid typed mission created state: %v", err)
			}
		})
	}
}

func TestTypedMissionErrorsDoNotExposeAuthorControlledValues(t *testing.T) {
	privateValue := "/private/synthetic/" + strings.Repeat("x", 8192)
	declaration := strings.Replace(validTypedMission, "dagr-load-workflow", privateValue, 1)
	dir := t.TempDir()
	manifestPath := writeTypedMissionFixture(t, dir, declaration)
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"validate", "--file", manifestPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("validate exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), privateValue) || strings.Contains(stderr.String(), "/private/") || len(stderr.String()) > 512 {
		t.Fatalf("diagnostic is not bounded and sanitized: %q", stderr.String())
	}
	for _, want := range []string{"mode=declaration-v1alpha1", "schema=platoon.dev/mission/v1alpha1", "reason=unknown-effect"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("diagnostic %q missing %q", stderr.String(), want)
		}
	}
}

func TestBlockingUnknownProducesDeterministicNotReadyPreview(t *testing.T) {
	declaration := strings.Replace(validTypedMission, "  unknowns: []", `  unknowns:
    - id: release-window
      question: Is the synthetic release window open?
      blocking: true
      attemptedSources: [mission-policy]
      route: mission-policy`, 1)
	declaration = strings.Replace(declaration, "  unknowns:", `    - id: release-owner
      source: mission-policy
      effects: []
      claim: owner-may-disposition
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
  unknowns:`, 1)
	dir := t.TempDir()
	manifestPath := writeTypedMissionFixture(t, dir, declaration)
	var first, second, stderr bytes.Buffer
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &first, &stderr); code != 0 {
		t.Fatalf("plan exit=%d stderr=%q", code, stderr.String())
	}
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &second, &stderr); code != 0 {
		t.Fatalf("second plan exit=%d stderr=%q", code, stderr.String())
	}
	if first.String() != second.String() || !strings.Contains(first.String(), `"ready": false`) ||
		!strings.Contains(first.String(), "blocking unknown release-window") {
		t.Fatalf("not-ready preview is not deterministic: %s", first.String())
	}
	if strings.Contains(first.String(), "Is the synthetic release window open?") || strings.Contains(first.String(), dir) {
		t.Fatalf("preview exposed source body or private path: %s", first.String())
	}
}

func TestReferenceMissionFormatNeverSniffsMissionContent(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("  mission: docs/mission.md\n"), []byte("  mission: typed-looking.yaml\n  missionFormat: reference\n"), 1)
	manifestPath := filepath.Join(dir, "platoon.yaml")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "typed-looking.yaml"), []byte("apiVersion: platoon.dev/mission/v999\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{"validate", "--file", manifestPath}, &stdout, &stderr); code != 0 || stdout.String() != "valid: synthetic-platoon\n" {
		t.Fatalf("explicit reference validate exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := os.Remove(filepath.Join(dir, "typed-looking.yaml")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Run([]string{"plan", "--file", manifestPath}, &stdout, &stderr); code != 0 || strings.Contains(stdout.String(), `"mission"`) {
		t.Fatalf("reference plan exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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

func TestValidateReportsOutputWriteFailure(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "platoon.yaml")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := cli.Run([]string{"validate", "--file", manifestPath}, failingWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "write output") {
		t.Fatalf("validate exit=%d stderr=%q", code, stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic write failure")
}

func writeTypedMissionFixture(t *testing.T, dir, declaration string) string {
	t.Helper()
	raw, err := os.ReadFile("../../examples/platoon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("  mission: docs/mission.md\n"), []byte("  mission: mission.yaml\n  missionFormat: declaration-v1alpha1\n"), 1)
	manifestPath := filepath.Join(dir, "platoon.yaml")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mission.yaml"), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

const validTypedMission = `apiVersion: platoon.dev/mission/v1alpha1
kind: Mission
metadata:
  name: synthetic-delivery
spec:
  objective: Deliver a synthetic API and its public guide.
  class: deliver
  effects:
    allowed: [dagr-load-workflow, dagr-start-run, dagr-ack-stage, sergeant-dispatch, run-validation, write-claimed-source]
    prohibited: []
    stages:
      build-api: [sergeant-dispatch, run-validation, write-claimed-source]
      write-guide: [sergeant-dispatch, run-validation, write-claimed-source]
      review-release: [sergeant-dispatch, run-validation]
    callers:
      dagr-load-workflow: [platoon]
      dagr-start-run: [platoon]
      dagr-ack-stage: [platoon]
      sergeant-dispatch: [platoon]
      run-validation: [platoon]
      write-claimed-source: [stage]
  stops: []
  authorityAssumptions:
    - id: dagr-authority
      source: dagr-authority
      effects: [dagr-load-workflow, dagr-start-run, dagr-ack-stage]
      claim: source-is-authoritative
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
    - id: sergeant-authority
      source: sergeant-authority
      effects: [sergeant-dispatch]
      claim: source-is-authoritative
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
    - id: validation-authority
      source: validation-authority
      effects: [run-validation]
      claim: source-is-authoritative
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
    - id: git-authority
      source: git-authority
      effects: [write-claimed-source]
      claim: source-is-authoritative
      revisionPolicy: exact
      expectedRevision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      route: mission-policy
    - id: platoon-attempt
      source: mission-policy
      effects: [dagr-load-workflow, dagr-start-run, dagr-ack-stage, sergeant-dispatch, run-validation]
      claim: actor-may-attempt
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
      actorRole: platoon
    - id: build-attempt
      source: mission-policy
      effects: [write-claimed-source]
      claim: actor-may-attempt
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
      actorRole: stage
      stage: build-api
    - id: guide-attempt
      source: mission-policy
      effects: [write-claimed-source]
      claim: actor-may-attempt
      revisionPolicy: exact
      expectedRevision: v1
      route: mission-policy
      actorRole: stage
      stage: write-guide
  unknowns: []
  contradictions: []
  outputs:
    - id: product-change
      category: product-delta
      stage: build-api
      schema: synthetic.product-delta/v1
      evidenceRoles: [mission-policy]
      gatesOutcome: completed
    - id: acceptance-result
      category: acceptance-evidence
      stage: review-release
      schema: synthetic.acceptance/v1
      evidenceRoles: [mission-policy]
      gatesOutcome: completed
  handoffs: []
  unattended:
    requested: false
  sources:
    - id: mission-policy
      kind: platoon-policy
      schema: platoon.policy/v1alpha1
      locator: public-policy
      revision: v1
      role: mission-policy
      reason: Defines the synthetic mission boundary.
    - id: dagr-authority
      kind: dagr
      schema: dagr.capability/v1
      locator: synthetic-dagr
      revision: v1
      role: dagr-authority
      reason: Defines the synthetic Dagr capability boundary.
    - id: sergeant-authority
      kind: sergeant
      schema: sergeant.mission-source/v1
      locator: synthetic-sergeant
      revision: v1
      role: sergeant-authority
      reason: Defines the synthetic Sergeant capability boundary.
    - id: validation-authority
      kind: validation-capability
      schema: platoon.validation-capability/v1alpha1
      locator: synthetic-validation
      revision: v1
      role: validation-authority
      reason: Defines the synthetic validation capability boundary.
    - id: git-authority
      kind: git
      schema: git.object/v1
      locator: synthetic-repository
      revision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      role: git-authority
      reason: Defines the synthetic repository boundary.
`
