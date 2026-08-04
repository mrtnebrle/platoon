package adapter

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestSergeantDispatchRequiresMatchingReceipt(t *testing.T) {
	executor := &sequenceExecutor{results: []Result{{Stdout: []byte("task-id: synthetic-fleet-a\nworker started\nTask ID: synthetic-fleet-a\n")}}}
	client := NewSergeantCLI(manifest.Command{Executable: "sgt-dispatch", Args: []string{"prefix"}}, executor, time.Minute, 4096)
	request := DispatchRequest{
		Project: "synthetic-project", Task: "task-build-api", Repository: "synthetic-api",
		Branch: "feat/synthetic-api", Harness: "opencode", Stage: "build-api",
		IntentFile: "intent.md", OriginProfile: "platoon-local", CorrelationID: "run-a-build-api",
	}
	fleetID, err := client.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if fleetID != "synthetic-fleet-a" {
		t.Fatalf("fleet ID = %q", fleetID)
	}
	want := []string{
		"prefix", "synthetic-project", "--td", "task-build-api", "--repos", "synthetic-api",
		"--branch", "feat/synthetic-api", "--agent", "opencode", "--stage", "build-api",
		"--intent-file", "intent.md", "--origin-profile", "platoon-local", "--correlation-id", "run-a-build-api",
	}
	if got := executor.invocations[0].Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch args = %#v, want %#v", got, want)
	}
}

func TestSergeantDispatchRejectsAmbiguousReceiptWithoutLeakingOutput(t *testing.T) {
	for name, output := range map[string]string{
		"missing final": "task-id: synthetic-fleet-a\nsecret-canary\n",
		"mismatch":      "task-id: synthetic-fleet-a\nTask ID: synthetic-fleet-b\nsecret-canary\n",
		"duplicate":     "task-id: synthetic-fleet-a\ntask-id: synthetic-fleet-a\nTask ID: synthetic-fleet-a\n",
	} {
		t.Run(name, func(t *testing.T) {
			executor := &sequenceExecutor{results: []Result{{Stdout: []byte(output)}}}
			client := NewSergeantCLI(manifest.Command{Executable: "sgt-dispatch"}, executor, time.Minute, 4096)
			_, err := client.Dispatch(context.Background(), DispatchRequest{Project: "p", Task: "t", Repository: "r", Branch: "b", Harness: "h", Stage: "s", IntentFile: "i", OriginProfile: "o", CorrelationID: "c"})
			if err == nil {
				t.Fatal("Dispatch() accepted ambiguous receipt")
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatalf("error leaked output: %v", err)
			}
		})
	}
}
