package adapter

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestWorkflowYAMLIsHooklessAndDeterministic(t *testing.T) {
	stages := []manifest.Stage{
		{ID: "second", DependsOn: []string{"first"}},
		{ID: "first"},
	}
	first, err := WorkflowYAML("platoon-run-a", stages)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkflowYAML("platoon-run-a", []manifest.Stage{stages[1], stages[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("workflow depends on manifest order:\n%s\n%s", first, second)
	}
	if strings.Contains(string(first), "hook:") || strings.Contains(string(first), "executor:") {
		t.Fatalf("workflow contains executable hook: %s", first)
	}
	if !strings.Contains(string(first), "depends-on:\n      - first") {
		t.Fatalf("workflow omitted dependency: %s", first)
	}
}

func TestDagrStartParsesFullIdentifiersAndUsesArgumentArrays(t *testing.T) {
	executor := &sequenceExecutor{results: []Result{
		{Stdout: []byte("created dag \"platoon-run-a\" (11111111-1111-4111-8111-111111111111)\n  added stage \"first\" (aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa)\n  added stage \"second\" (bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb)\nloaded workflow \"platoon-run-a\"\n")},
		{Stdout: []byte("ID                                    NAME                  BRIEF                                   \naaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa  first                                                         \nbbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb  second                                                        \n")},
		{Stdout: []byte("started run cccccccc-cccc-4ccc-8ccc-cccccccccccc for dag \"platoon-run-a\"\n")},
	}}
	client := NewDagrCLI(manifest.DagrAdapter{Executable: "dagr", Args: []string{"--quiet"}, Database: "state/dagr.db"}, executor, time.Minute, 4096)
	got, err := client.Start(context.Background(), "workflow.yaml", "platoon-run-a", []string{"first", "second"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	want := DagrRun{
		RunID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		StageIDs: map[string]string{
			"first":  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"second": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Start() = %#v, want %#v", got, want)
	}
	wantInvocations := [][]string{
		{"--quiet", "--db", "state/dagr.db", "workflow", "load", "workflow.yaml"},
		{"--quiet", "--db", "state/dagr.db", "stage", "list", "platoon-run-a"},
		{"--quiet", "--db", "state/dagr.db", "run", "start", "platoon-run-a"},
	}
	for i, invocation := range executor.invocations {
		if !reflect.DeepEqual(invocation.Args, wantInvocations[i]) {
			t.Errorf("invocation %d args = %#v, want %#v", i, invocation.Args, wantInvocations[i])
		}
	}
}

func TestDagrSnapshotRejectsTruncatedOrUnknownStages(t *testing.T) {
	valid := "┌─ synthetic                   run:cccccccc ─┐\n" +
		"│    first                            ○ ready          │\n" +
		"│    second                           ◌ pending        │\n" +
		"└────────────────────────────────────────────┘\n"
	executor := &sequenceExecutor{results: []Result{{Stdout: []byte(valid)}}}
	client := NewDagrCLI(manifest.DagrAdapter{Executable: "dagr", Database: "dagr.db"}, executor, time.Minute, 4096)
	got, err := client.Snapshot(context.Background(), "cccccccc-cccc-4ccc-8ccc-cccccccccccc", []string{"first", "second"})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got["first"] != DagrReady || got["second"] != DagrPending {
		t.Fatalf("Snapshot() = %#v", got)
	}

	executor.results = []Result{{Stdout: []byte(strings.Replace(valid, "second", "unexpected", 1))}}
	executor.index = 0
	if _, err := client.Snapshot(context.Background(), "cccccccc-cccc-4ccc-8ccc-cccccccccccc", []string{"first", "second"}); err == nil {
		t.Fatal("Snapshot() accepted missing/unknown stage")
	}
}

func TestDagrTerminalTransitionRequiresExactAcknowledgement(t *testing.T) {
	executor := &sequenceExecutor{results: []Result{{Stdout: []byte("step done: run=cccccccc-cccc-4ccc-8ccc-cccccccccccc stage=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\n")}}}
	client := NewDagrCLI(manifest.DagrAdapter{Executable: "dagr", Database: "dagr.db"}, executor, time.Minute, 4096)
	if err := client.SetTerminal(context.Background(), "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", true); err != nil {
		t.Fatalf("SetTerminal() error = %v", err)
	}
}

func TestDagrRecoveryUsesReadOnlyFullIDInspection(t *testing.T) {
	executor := &sequenceExecutor{results: []Result{{Stdout: []byte("cccccccc-cccc-4ccc-8ccc-cccccccccccc\n")}}}
	client := NewDagrCLI(manifest.DagrAdapter{
		Executable: "dagr", Database: "dagr.db", InspectExecutable: "sqlite3",
	}, executor, time.Minute, 4096)
	recovery, err := client.RecoverRun(context.Background(), "platoon-run-a")
	if err != nil {
		t.Fatal(err)
	}
	if recovery.State != DagrRunFound || recovery.RunID != "cccccccc-cccc-4ccc-8ccc-cccccccccccc" {
		t.Fatalf("RecoverRun() = %#v", recovery)
	}
	invocation := executor.invocations[0]
	if invocation.Executable != "sqlite3" || len(invocation.Args) < 4 || invocation.Args[0] != "-readonly" {
		t.Fatalf("recovery invocation = %#v", invocation)
	}
}

type sequenceExecutor struct {
	results     []Result
	errors      []error
	invocations []Invocation
	index       int
}

func (s *sequenceExecutor) Run(_ context.Context, invocation Invocation) (Result, error) {
	s.invocations = append(s.invocations, invocation)
	index := s.index
	s.index++
	if index < len(s.errors) && s.errors[index] != nil {
		return Result{}, s.errors[index]
	}
	return s.results[index], nil
}
