package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/missioncontrol"
)

func TestMissionSourcesObserveLocalGitAndDagrCapabilities(t *testing.T) {
	database := filepath.Join(t.TempDir(), "dagr.db")
	if err := os.WriteFile(database, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	m := &manifest.Manifest{Spec: manifest.Spec{
		Limits:       manifest.Limits{CommandTimeout: "1s"},
		Adapters:     manifest.Adapters{Dagr: manifest.DagrAdapter{Database: database, InspectExecutable: "sqlite3"}},
		Repositories: []manifest.Repository{{ID: "synthetic-repository", Path: t.TempDir()}},
	}}
	executor := &sequenceExecutor{results: []Result{{Stdout: []byte(revision + "\n")}, {Stdout: []byte("1\n")}}}
	now := func() time.Time { return time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC) }
	sources := &MissionSources{manifest: m, executor: executor, now: now}

	gitObservation, err := sources.Query(context.Background(), missioncontrol.SourceQuery{
		SourceID: "git-source", Kind: "git", Schema: "git.object/v1", Locator: "synthetic-repository", ExpectedRevision: revision,
	})
	if err != nil || gitObservation.Quality != missioncontrol.QualityVerified || gitObservation.Payload["objectId"] != revision {
		t.Fatalf("git observation=%#v err=%v", gitObservation, err)
	}
	dagrObservation, err := sources.Query(context.Background(), missioncontrol.SourceQuery{
		SourceID: "dagr-source", Kind: "dagr", Schema: "dagr.capability/v1", Locator: "synthetic-dagr", ExpectedRevision: "v1",
	})
	if err != nil || dagrObservation.Quality != missioncontrol.QualityVerified || dagrObservation.Payload["schemaVersion"] != "1" {
		t.Fatalf("dagr observation=%#v err=%v", dagrObservation, err)
	}
	policyObservation, err := sources.Query(context.Background(), missioncontrol.SourceQuery{
		SourceID: "policy-source", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", Locator: "synthetic-policy", ExpectedRevision: "v1",
	})
	if err != nil || policyObservation.Quality != missioncontrol.QualityVerified || len(policyObservation.Payload["policyDigest"].(string)) != 64 {
		t.Fatalf("policy observation=%#v err=%v", policyObservation, err)
	}
	if len(executor.invocations) != 2 {
		t.Fatalf("read-only adapter invocations=%#v", executor.invocations)
	}
}
