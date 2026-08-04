package commander

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/adapter"
	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/state"
	"gopkg.in/yaml.v3"
)

func TestStartAtomicallyHonorsTokenLimits(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("one", "internal/one", "one-contract"),
		implementationStage("two", "internal/two", "two-contract"),
		implementationStage("three", "internal/three", "three-contract"),
	)
	m.Spec.Limits.Implementation = 2

	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := fixture.dispatcher.count(); got != 2 {
		t.Fatalf("dispatch count = %d, want 2", got)
	}
	active, queued := 0, 0
	for _, stage := range run.Stages {
		switch stage.Status {
		case state.StageDispatched:
			active++
		case state.StageQueued:
			queued++
		}
	}
	if active != 2 || queued != 1 {
		t.Fatalf("stage accounting active=%d queued=%d: %#v", active, queued, run.Stages)
	}
}

func TestStartUsesValidatedManifestByteSnapshotAfterSourceReplacement(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	input := fixture.startInput()
	expected := sha256.Sum256(input.ManifestBytes)
	if err := os.WriteFile(fixture.manifestPath, []byte("replaced source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.commander.Start(context.Background(), m, input)
	if err != nil {
		t.Fatal(err)
	}
	if run.ManifestSourceDigest != hex.EncodeToString(expected[:]) {
		t.Fatalf("manifest source digest = %q", run.ManifestSourceDigest)
	}
}

func TestStartRejectsManifestObjectThatDoesNotMatchSnapshot(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	input := fixture.startInput()
	m.Metadata.Name = "different-platoon"
	if _, err := fixture.commander.Start(context.Background(), m, input); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched manifest error = %v", err)
	}
}

func TestReconcileRecoversLostDispatchReceiptWithoutRedispatch(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	fixture.dispatcher.failAfterCreate = true

	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil {
		t.Fatal("Start() succeeded despite lost dispatch receipt")
	}
	if run.Stages["build-api"].Status != state.StageReconcileRequired {
		t.Fatalf("stage status = %q", run.Stages["build-api"].Status)
	}
	fixture.dispatcher.failAfterCreate = false

	reconciled, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := fixture.dispatcher.count(); got != 1 {
		t.Fatalf("dispatch count = %d, duplicate dispatch occurred", got)
	}
	stage := reconciled.Stages["build-api"]
	if stage.FleetID != "fleet-build-api" || stage.Reservation.Phase != state.ReservationCommitted {
		t.Fatalf("recovered stage = %#v", stage)
	}
}

func TestReconcileRetriesOnceAfterBoundedDispatchAbsenceProof(t *testing.T) {
	fixture := newFixture(t)
	fixture.dispatcher.failBeforeCreate = 1
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil {
		t.Fatal("Start() unexpectedly passed pre-child dispatch failure")
	}
	if run.Stages["build-api"].Reservation.DispatchAttempts != 1 {
		t.Fatalf("first dispatch attempt was not journaled: %#v", run.Stages["build-api"].Reservation)
	}
	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Stages["build-api"].Reservation.Phase != state.ReservationCommitted || fixture.dispatcher.count() != 2 {
		t.Fatalf("bounded absence recovery = %#v dispatches=%d", recovered.Stages["build-api"], fixture.dispatcher.count())
	}
}

func TestDispatchAbsenceExhaustionReleasesReservationWithoutChild(t *testing.T) {
	fixture := newFixture(t)
	fixture.dispatcher.failBeforeCreate = 2
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil {
		t.Fatal("Start() unexpectedly passed first absent dispatch")
	}
	run, err = fixture.commander.Reconcile(context.Background(), run.ID)
	if err == nil {
		t.Fatal("first recovery unexpectedly passed second absent dispatch")
	}
	released, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stage := released.Stages["build-api"]
	if stage.Reservation.Phase != state.ReservationAbsent || stage.FleetID != "" || stage.Status != state.StageBlocked {
		t.Fatalf("absence exhaustion retained reservation: %#v", stage)
	}
	if fixture.dispatcher.count() != 2 {
		t.Fatalf("bounded recovery dispatched %d times", fixture.dispatcher.count())
	}
}

func TestReconcileDispatchesPreparedReservationExactlyOnce(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.store.AcquireLease(fixture.leaseOptions)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.LoadRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Generation = lease.Generation()
	stage := prepared.Stages["build-api"]
	stage.FleetID = ""
	stage.Status = state.StageReserved
	stage.Reservation.Phase = state.ReservationPrepared
	stage.Reservation.FleetID = ""
	if err := fixture.store.SaveRun(prepared, lease); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	fixture.dispatcher.reset()

	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.dispatcher.count() != 1 || recovered.Stages["build-api"].Reservation.Phase != state.ReservationCommitted {
		t.Fatalf("prepared recovery = %#v dispatches=%d", recovered.Stages["build-api"], fixture.dispatcher.count())
	}
}

func TestReconcileStartsPreparedDagrRunOnlyAfterAbsenceProof(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.store.AcquireLease(fixture.leaseOptions)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := fixture.store.LoadRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Generation = lease.Generation()
	prepared.Dagr.Phase = "prepared"
	prepared.Dagr.RunID = ""
	prepared.Status = state.RunInitialized
	if err := fixture.store.SaveRun(prepared, lease); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	fixture.dagr.resetStartCalls()

	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Dagr.Phase != "active" || fixture.dagr.startCallCount() != 1 {
		t.Fatalf("prepared dagr recovery = %#v calls=%d", recovered.Dagr, fixture.dagr.startCallCount())
	}
}

func TestReconcileRecoversWorkflowMutationAfterLostLoadReceipt(t *testing.T) {
	fixture := newFixture(t)
	fixture.dagr.failLoad = 1
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil || run.Dagr.Phase != "loading_workflow" {
		t.Fatalf("lost load receipt was not journaled: phase=%q err=%v", run.Dagr.Phase, err)
	}
	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Dagr.Phase != "active" || fixture.dispatcher.count() != 1 {
		t.Fatalf("workflow recovery did not converge: %#v dispatches=%d", recovered.Dagr, fixture.dispatcher.count())
	}
}

func TestReconcileRecoversStageDiscoveryAfterLostListReceipt(t *testing.T) {
	fixture := newFixture(t)
	fixture.dagr.failList = 1
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil || run.Dagr.Phase != "workflow_loaded" {
		t.Fatalf("lost stage receipt was not journaled: phase=%q err=%v", run.Dagr.Phase, err)
	}
	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Dagr.Phase != "active" || fixture.dispatcher.count() != 1 {
		t.Fatalf("stage discovery recovery did not converge: %#v", recovered.Dagr)
	}
}

func TestStartReturnsReloadedDurableStateAfterConfirmingSaveFails(t *testing.T) {
	fixture := newFixture(t)
	runDir, err := fixture.store.RunDir("run-a")
	if err != nil {
		t.Fatal(err)
	}
	fixture.dagr.afterStart = func() {
		if err := os.Chmod(runDir, 0o500); err != nil {
			fixture.t.Errorf("make run directory read-only: %v", err)
		}
	}
	defer os.Chmod(runDir, 0o700)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil {
		t.Fatal("Start() succeeded despite failed confirming state write")
	}
	if run == nil || run.Dagr.Phase != "starting_run" || run.Dagr.RunID != "" {
		t.Fatalf("returned state was not durable authority: %#v", run)
	}
}

func TestStartAdoptsVerifiedFleetAndAccountsForItsToken(t *testing.T) {
	fixture := newFixture(t)
	adopted := implementationStage("adopted", "internal/adopted", "adopted-contract")
	adopted.AdoptFleet = "existing-fleet"
	next := implementationStage("next", "internal/next", "next-contract")
	m := fixture.manifest(adopted, next)
	m.Spec.Limits.Implementation = 1
	fixture.fleets.put(adapter.FleetEvidence{
		FleetID: "existing-fleet", Repository: "synthetic-repo", Status: adapter.FleetInProgress,
		Worktree: "worktree-adopted", InitialSHA: strings.Repeat("a", 40), IntentRevision: fixture.intentRevision,
	})

	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := fixture.dispatcher.count(); got != 0 {
		t.Fatalf("dispatch count = %d, adopted work did not consume capacity", got)
	}
	if !run.Stages["adopted"].Adopted || run.Stages["next"].Status != state.StageQueued {
		t.Fatalf("adoption accounting = %#v", run.Stages)
	}
}

func TestTerminalAdoptedDependentWaitsForDagrReadiness(t *testing.T) {
	fixture := newFixture(t)
	first := implementationStage("first", "internal/first", "first-contract")
	adopted := implementationStage("adopted", "internal/adopted", "adopted-contract")
	adopted.DependsOn = []string{"first"}
	adopted.AdoptFleet = "existing-adopted"
	last := implementationStage("last", "internal/last", "last-contract")
	last.DependsOn = []string{"adopted"}
	m := fixture.manifest(first, adopted, last)
	fixture.fleets.put(adapter.FleetEvidence{
		FleetID: "existing-adopted", Repository: "synthetic-repo", Status: adapter.FleetDone,
		ResultDigest: strings.Repeat("e", 64), Worktree: "worktree-adopted",
		InitialSHA: strings.Repeat("a", 40), IntentRevision: fixture.intentRevision,
	})
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Stages["adopted"].Status == state.StageDone || fixture.integrator.runCalls != 0 || fixture.dagr.terminalCalls != 0 {
		t.Fatalf("pending adopted stage advanced: stage=%#v integration=%d dagr=%d", blocked.Stages["adopted"], fixture.integrator.runCalls, fixture.dagr.terminalCalls)
	}
	if fixture.dispatcher.count() != 1 || blocked.Stages["last"].FleetID != "" {
		t.Fatalf("descendant advanced before adopted readiness: %#v", blocked.Stages)
	}
	fixture.fleets.setStatus("fleet-first", adapter.FleetDone)
	afterFirst, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Stages["adopted"].Status == state.StageDone || fixture.dagr.terminalCalls != 1 {
		t.Fatalf("adopted stage advanced in stale readiness cycle: %#v", afterFirst.Stages["adopted"])
	}
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if fixture.dagr.terminalCalls != 2 {
		t.Fatalf("ready adopted stage did not advance exactly once: %d", fixture.dagr.terminalCalls)
	}
}

func TestConflictingAdoptedClaimsBlockFurtherAdmission(t *testing.T) {
	fixture := newFixture(t)
	first := implementationStage("adopt-a", "internal/shared", "shared-contract")
	first.AdoptFleet = "existing-a"
	second := implementationStage("adopt-b", "internal/shared/child", "other-contract")
	second.AdoptFleet = "existing-b"
	next := implementationStage("next", "internal/next", "next-contract")
	m := fixture.manifest(first, second, next)
	m.Spec.Limits.Implementation = 3
	for _, item := range []struct {
		fleet, worktree string
	}{
		{"existing-a", "worktree-adopt-a"},
		{"existing-b", "worktree-adopt-b"},
	} {
		fixture.fleets.put(adapter.FleetEvidence{
			FleetID: item.fleet, Repository: "synthetic-repo", Status: adapter.FleetInProgress,
			Worktree: item.worktree, InitialSHA: strings.Repeat("a", 40), IntentRevision: fixture.intentRevision,
		})
	}

	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if run.Status != state.RunReconcileRequired || fixture.dispatcher.count() != 0 {
		t.Fatalf("conflicting adoption did not block: status=%q dispatches=%d", run.Status, fixture.dispatcher.count())
	}
}

func TestOutOfClaimDiffBlocksDagrWithoutMutatingFleet(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.diff.paths["worktree-build-api"] = []adapter.ChangedPath{{Path: "outside/file.go"}}

	reconciled, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	stage := reconciled.Stages["build-api"]
	if stage.Status != state.StageOutOfClaim || stage.Reservation.Phase != state.ReservationReleased {
		t.Fatalf("stage = %#v", stage)
	}
	if fixture.dagr.terminalCalls != 0 || fixture.integrator.runCalls != 0 {
		t.Fatalf("out-of-claim work advanced: dagr=%d integration=%d", fixture.dagr.terminalCalls, fixture.integrator.runCalls)
	}
	if got := fixture.fleets.status("fleet-build-api"); got != adapter.FleetDone {
		t.Fatalf("Commander mutated child status to %q", got)
	}
}

func TestQueuedCandidateCannotAdvanceAfterLaterOutOfClaimEvidence(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("worker", "internal/worker", "worker-contract"),
		implementationStage("api", "internal/api", "api-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
	fixture.fleets.setStatus("fleet-worker", adapter.FleetDone)
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	fixture.diff.paths["worktree-worker"] = []adapter.ChangedPath{{Path: "outside/worker.go"}}
	reconciled, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Stages["worker"].Status != state.StageOutOfClaim ||
		reconciled.MergeQueue["synthetic-repo"][1].Status != state.CandidateBlocked {
		t.Fatalf("later violation did not invalidate queued candidate: %#v", reconciled)
	}
	if fixture.integrator.runCalls != 1 || fixture.dagr.terminalCalls != 1 {
		t.Fatalf("invalid candidate advanced: integrations=%d dagr=%d", fixture.integrator.runCalls, fixture.dagr.terminalCalls)
	}
}

func TestOutOfClaimStageFreezesAllRunIntegration(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("api", "internal/api", "api-contract"),
		implementationStage("worker", "internal/worker", "worker-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
	fixture.fleets.setStatus("fleet-worker", adapter.FleetDone)
	fixture.diff.paths["worktree-worker"] = []adapter.ChangedPath{{Path: "outside/worker.go"}}
	blocked, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Stages["worker"].Status != state.StageOutOfClaim || fixture.integrator.runCalls != 0 || fixture.dagr.terminalCalls != 0 {
		t.Fatalf("out-of-claim run advanced: stages=%#v integration=%d dagr=%d", blocked.Stages, fixture.integrator.runCalls, fixture.dagr.terminalCalls)
	}
}

func TestNewOutOfClaimEvidenceBlocksPendingDagrReplay(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("api", "internal/api", "api-contract"),
		implementationStage("worker", "internal/worker", "worker-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
	fixture.dagr.failTerminal = 1
	pending, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err == nil || pending.Stages["api"].DagrTerminalPending != "done" {
		t.Fatalf("pending transition not journaled: %#v err=%v", pending.Stages["api"], err)
	}
	fixture.fleets.setStatus("fleet-worker", adapter.FleetDone)
	fixture.diff.paths["worktree-worker"] = []adapter.ChangedPath{{Path: "outside/worker.go"}}
	blocked, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Stages["worker"].Status != state.StageOutOfClaim || fixture.dagr.terminalCalls != 0 {
		t.Fatalf("pending dagr replay bypassed new violation: stages=%#v dagr=%d", blocked.Stages, fixture.dagr.terminalCalls)
	}
}

func TestNonSuccessOutOfClaimChildFreezesRun(t *testing.T) {
	for _, childStatus := range []adapter.FleetStatus{adapter.FleetInProgress, adapter.FleetFailed} {
		t.Run(string(childStatus), func(t *testing.T) {
			fixture := newFixture(t)
			m := fixture.manifest(
				implementationStage("api", "internal/api", "api-contract"),
				implementationStage("worker", "internal/worker", "worker-contract"),
			)
			m.Spec.Limits.Implementation = 2
			m.Spec.Repositories[0].MaxWriters = 2
			run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
			if err != nil {
				t.Fatal(err)
			}
			fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
			fixture.fleets.setStatus("fleet-worker", childStatus)
			fixture.diff.paths["worktree-worker"] = []adapter.ChangedPath{{Path: "outside/worker.go"}}
			blocked, err := fixture.commander.Reconcile(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if blocked.Stages["worker"].Status != state.StageOutOfClaim || fixture.integrator.runCalls != 0 || fixture.dagr.terminalCalls != 0 {
				t.Fatalf("non-success violation advanced: status=%s stages=%#v integration=%d dagr=%d", childStatus, blocked.Stages, fixture.integrator.runCalls, fixture.dagr.terminalCalls)
			}
		})
	}
}

func TestRecoveredOutOfClaimChildFreezesRunBeforeTransitions(t *testing.T) {
	for _, childStatus := range []adapter.FleetStatus{adapter.FleetInProgress, adapter.FleetFailed} {
		t.Run(string(childStatus), func(t *testing.T) {
			fixture := newFixture(t)
			fixture.dispatcher.failAfterCreate = true
			m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
			run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
			if err == nil {
				t.Fatal("Start() unexpectedly passed lost receipt")
			}
			fixture.dispatcher.failAfterCreate = false
			fixture.fleets.setStatus("fleet-build-api", childStatus)
			fixture.diff.paths["worktree-build-api"] = []adapter.ChangedPath{{Path: "outside/recovered.go"}}
			blocked, err := fixture.commander.Reconcile(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if blocked.Stages["build-api"].Status != state.StageOutOfClaim || fixture.integrator.runCalls != 0 || fixture.dagr.terminalCalls != 0 {
				t.Fatalf("recovered violation advanced: status=%s stage=%#v integration=%d dagr=%d", childStatus, blocked.Stages["build-api"], fixture.integrator.runCalls, fixture.dagr.terminalCalls)
			}
		})
	}
}

func TestIntegrationCreatedOutOfClaimPathBlocksAdvancement(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.integrator.afterRun = func() {
		fixture.diff.paths["worktree-build-api"] = []adapter.ChangedPath{{Path: "outside/generated.go"}}
	}
	reconciled, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Stages["build-api"].Status != state.StageOutOfClaim || fixture.dagr.terminalCalls != 0 {
		t.Fatalf("post-command violation advanced: stage=%#v dagr=%d", reconciled.Stages["build-api"], fixture.dagr.terminalCalls)
	}
}

func TestFailedIntegrationStillChecksPostCommandClaims(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.integrator.runError = errors.New("synthetic command failure")
	fixture.integrator.afterRun = func() {
		fixture.diff.paths["worktree-build-api"] = []adapter.ChangedPath{{Path: "outside/generated.go"}}
	}
	reconciled, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Stages["build-api"].Status != state.StageOutOfClaim || fixture.dagr.terminalCalls != 0 {
		t.Fatalf("failed command bypassed final claims: stage=%#v dagr=%d", reconciled.Stages["build-api"], fixture.dagr.terminalCalls)
	}
}

func TestAlreadyStaleCandidateWaitsForCurrentBaseAncestry(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.integrator.containsBase = false
	queued, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.MergeQueue["synthetic-repo"][0].Status != state.CandidateQueued || fixture.integrator.runCalls != 0 {
		t.Fatalf("stale candidate integrated: %#v", queued.MergeQueue)
	}
	fixture.integrator.containsBase = true
	completed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != state.RunCompleted || fixture.integrator.runCalls != 1 {
		t.Fatalf("current-base candidate did not complete: status=%q calls=%d", completed.Status, fixture.integrator.runCalls)
	}
}

func TestDispatchReceiptRequiresMatchingDurableCorrelation(t *testing.T) {
	fixture := newFixture(t)
	fixture.dispatcher.wrongCorrelation = true
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil {
		t.Fatal("Start() committed mismatched dispatch correlation")
	}
	if run.Stages["build-api"].Status != state.StageReconcileRequired || run.Stages["build-api"].FleetID != "" {
		t.Fatalf("mismatched receipt was committed: %#v", run.Stages["build-api"])
	}
}

func TestLaterAdmissionUsesAndVerifiesRunOwnedIntentSnapshot(t *testing.T) {
	fixture := newFixture(t)
	first := implementationStage("first", "internal/first", "first-contract")
	second := implementationStage("second", "internal/second", "second-contract")
	second.DependsOn = []string{"first"}
	m := fixture.manifest(first, second)
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	if run.IntentPath == fixture.intentPath || fixture.dispatcher.intentFiles()[0] != run.IntentPath {
		t.Fatalf("dispatch did not use run-owned intent: run=%q requests=%#v", run.IntentPath, fixture.dispatcher.intentFiles())
	}
	if err := os.WriteFile(run.IntentPath, []byte("tampered intent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LoadRun(run.ID); err == nil {
		t.Fatal("LoadRun() accepted a mismatched run-owned intent")
	}
	fixture.fleets.setStatus("fleet-first", adapter.FleetDone)
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); err == nil {
		t.Fatal("Reconcile() admitted work after run-owned intent changed")
	}
	if fixture.dispatcher.count() != 1 {
		t.Fatalf("tampered intent caused dispatch count %d", fixture.dispatcher.count())
	}
}

func TestMergeQueueIntegratesOneCandidatePerRepository(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("worker", "internal/worker", "worker-contract"),
		implementationStage("api", "internal/api", "api-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
	fixture.fleets.setStatus("fleet-worker", adapter.FleetDone)

	first, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if fixture.integrator.runCalls != 1 {
		t.Fatalf("integration calls = %d, want one per repository", fixture.integrator.runCalls)
	}
	if got := candidateStatuses(first, "synthetic-repo"); !reflect.DeepEqual(got, []state.CandidateStatus{state.CandidateMergeReady, state.CandidateQueued}) {
		t.Fatalf("candidate statuses = %#v", got)
	}
	if first.MergeQueue["synthetic-repo"][0].Stage != "api" {
		t.Fatalf("merge ordering followed manifest order instead of deterministic priority: %#v", first.MergeQueue)
	}

	second, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if fixture.integrator.runCalls != 2 || second.Status != state.RunCompleted {
		t.Fatalf("second reconciliation calls=%d status=%q", fixture.integrator.runCalls, second.Status)
	}
	if fixture.integrator.mergeCalls != 0 || fixture.integrator.pushCalls != 0 {
		t.Fatal("Commander merged or pushed automatically")
	}
}

func TestSameRepositoryStagesUseDistinctIssueOwnedBranches(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("api", "internal/api", "api-contract"),
		implementationStage("worker", "internal/worker", "worker-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	if _, err := fixture.commander.Start(context.Background(), m, fixture.startInput()); err != nil {
		t.Fatal(err)
	}
	branches := fixture.dispatcher.branches()
	want := []string{"feat/synthetic-work-api", "feat/synthetic-work-worker"}
	if !reflect.DeepEqual(branches, want) {
		t.Fatalf("dispatch branches = %#v, want %#v", branches, want)
	}
}

func TestReconcileRetriesUncertainDagrTerminalTransition(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.dagr.failTerminal = 1
	failed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err == nil {
		t.Fatal("Reconcile() succeeded despite uncertain dagr transition")
	}
	if failed.Stages["build-api"].DagrTerminalPending != "done" {
		t.Fatalf("pending terminal proof = %#v", failed.Stages["build-api"])
	}

	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("recovery Reconcile() error = %v", err)
	}
	if recovered.Status != state.RunCompleted || recovered.Stages["build-api"].DagrTerminalPending != "" {
		t.Fatalf("recovered run = %#v", recovered)
	}
	if fixture.integrator.runCalls != 1 {
		t.Fatalf("integration reran %d times, want exactly once", fixture.integrator.runCalls)
	}
}

func TestDagrRecoveryChecksBeforeRepeatingTerminalMutation(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.dagr.failAfterTerminal = 1
	failed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err == nil || failed.Stages["build-api"].DagrTerminalPending != "done" {
		t.Fatalf("lost acknowledgement was not journaled: run=%#v error=%v", failed, err)
	}
	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != state.RunCompleted || fixture.dagr.terminalCalls != 1 {
		t.Fatalf("terminal mutation repeated: status=%q calls=%d", recovered.Status, fixture.dagr.terminalCalls)
	}
}

func TestPendingCompletionRequeuesAfterRepositoryBaseChanges(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.dagr.failTerminal = 1
	failed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err == nil || failed.Stages["build-api"].DagrTerminalPending != "done" {
		t.Fatalf("terminal uncertainty not journaled: run=%#v err=%v", failed, err)
	}
	fixture.integrator.mu.Lock()
	fixture.integrator.head = strings.Repeat("e", 40)
	fixture.integrator.mu.Unlock()
	queued, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.MergeQueue["synthetic-repo"][0].Status != state.CandidateQueued || fixture.integrator.runCalls != 1 {
		t.Fatalf("changed base did not requeue pending completion: %#v", queued.MergeQueue)
	}
	completed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != state.RunCompleted || fixture.integrator.runCalls != 2 {
		t.Fatalf("requeued pending completion did not rerun: status=%q calls=%d", completed.Status, fixture.integrator.runCalls)
	}
}

func TestBaseChangeDuringIntegrationRequeuesCandidate(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	baseA := strings.Repeat("a", 40)
	baseB := strings.Repeat("b", 40)
	fixture.integrator.heads = []string{baseA, baseB, baseB, baseB}

	first, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.MergeQueue["synthetic-repo"][0].Status != state.CandidateQueued || fixture.integrator.runCalls != 1 {
		t.Fatalf("stale candidate was not requeued: %#v", first.MergeQueue)
	}
	second, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != state.RunCompleted || fixture.integrator.runCalls != 2 {
		t.Fatalf("requeued candidate did not rerun: status=%q calls=%d", second.Status, fixture.integrator.runCalls)
	}
}

func TestCompletedRunReopensStaleMergeReadyCandidate(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	completed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil || completed.Status != state.RunCompleted {
		t.Fatalf("initial completion: status=%q err=%v", completed.Status, err)
	}
	fixture.integrator.mu.Lock()
	fixture.integrator.head = strings.Repeat("f", 40)
	fixture.integrator.mu.Unlock()
	reopened, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := reopened.MergeQueue["synthetic-repo"][0]
	if reopened.Status == state.RunCompleted || candidate.Status != state.CandidateQueued || fixture.integrator.runCalls != 1 {
		t.Fatalf("terminal stale candidate was not reopened: status=%q candidate=%#v calls=%d", reopened.Status, candidate, fixture.integrator.runCalls)
	}
	refreshed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil || refreshed.Status != state.RunCompleted || fixture.integrator.runCalls != 2 {
		t.Fatalf("reopened candidate did not reintegrate: status=%q calls=%d err=%v", refreshed.Status, fixture.integrator.runCalls, err)
	}
}

func TestActiveRunReopensCompletedStageAfterBaseChange(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("api", "internal/api", "api-contract"),
		implementationStage("worker", "internal/worker", "worker-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-api", adapter.FleetDone)
	active, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil || active.Status == state.RunCompleted || active.Stages["api"].Status != state.StageDone {
		t.Fatalf("expected partially active run: %#v err=%v", active, err)
	}
	fixture.integrator.mu.Lock()
	fixture.integrator.head = strings.Repeat("f", 40)
	fixture.integrator.mu.Unlock()
	reopened, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.MergeQueue["synthetic-repo"][0].Status != state.CandidateQueued || reopened.Stages["api"].Status != state.StageCandidate {
		t.Fatalf("active run retained stale merge-ready evidence: %#v", reopened)
	}
}

func TestTwoStaleCandidatesRequeueWithoutOrphaningGlobalClaim(t *testing.T) {
	authority, err := state.OpenAuthorityAt(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := newFixture(t)
	fixture.commander.authority = authority
	m := fixture.manifest(
		implementationStage("api", "internal/api", "api-contract"),
		implementationStage("worker", "internal/worker", "worker-contract"),
	)
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 1
	m.Spec.Repositories[0].Path = t.TempDir()
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	firstStage := dispatchedStage(run)
	fixture.fleets.setStatus("fleet-"+firstStage, adapter.FleetDone)
	run, err = fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondStage := "api"
	if firstStage == secondStage {
		secondStage = "worker"
	}
	if run.Stages[secondStage].FleetID == "" {
		run, err = fixture.commander.Reconcile(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.fleets.setStatus("fleet-"+secondStage, adapter.FleetDone)
	run, err = fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil || run.Status != state.RunCompleted {
		t.Fatalf("initial sequential completion status=%q err=%v", run.Status, err)
	}
	fixture.integrator.mu.Lock()
	fixture.integrator.head = strings.Repeat("f", 40)
	fixture.integrator.mu.Unlock()
	reopened, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range reopened.MergeQueue["synthetic-repo"] {
		if candidate.Status != state.CandidateQueued || reopened.Stages[candidate.Stage].GlobalClaimID != "" {
			t.Fatalf("stale candidate reserved before local requeue: candidate=%#v stage=%#v", candidate, reopened.Stages[candidate.Stage])
		}
	}
	afterFirst, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Status == state.RunCompleted {
		t.Fatal("both one-writer candidates integrated in one cycle")
	}
	completed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil || completed.Status != state.RunCompleted || fixture.integrator.runCalls != 4 {
		t.Fatalf("stale candidates did not converge: status=%q calls=%d err=%v", completed.Status, fixture.integrator.runCalls, err)
	}
}

func TestConcurrentReconciliationCannotDoubleAdmit(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("first", "internal/first", "first-contract"),
		implementationStage("second", "internal/second", "second-contract"),
	)
	m.Spec.Limits.Implementation = 1
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	active := dispatchedStage(run)
	fixture.fleets.setStatus("fleet-"+active, adapter.FleetDone)
	fixture.integrator.entered = make(chan struct{})
	fixture.integrator.release = make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, err := fixture.commander.Reconcile(context.Background(), run.ID)
		firstResult <- err
	}()
	select {
	case <-fixture.integrator.entered:
	case <-time.After(time.Second):
		t.Fatal("first reconciliation did not reach integration")
	}
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); !errors.Is(err, state.ErrLeaseHeld) {
		t.Fatalf("concurrent reconciliation error = %v, want ErrLeaseHeld", err)
	}
	close(fixture.integrator.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if fixture.dispatcher.count() != 2 {
		t.Fatalf("concurrent reconciliation dispatch count = %d, want 2 total", fixture.dispatcher.count())
	}
}

func TestSeparateStateRootsRejectOverlappingGlobalClaims(t *testing.T) {
	authority, err := state.OpenAuthorityAt(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	repositoryAlias := filepath.Join(t.TempDir(), "repository-alias")
	if err := os.Symlink(repository, repositoryAlias); err != nil {
		t.Fatal(err)
	}
	first := newFixture(t)
	second := newFixture(t)
	first.commander.authority = authority
	second.commander.authority = authority
	firstManifest := first.manifest(implementationStage("first", "internal/shared", "shared-contract"))
	secondManifest := second.manifest(implementationStage("second", "internal/shared/child", "other-contract"))
	firstManifest.Spec.Repositories[0].Path = repository
	secondManifest.Spec.Repositories[0].Path = repositoryAlias
	if _, err := first.commander.Start(context.Background(), firstManifest, first.startInput()); err != nil {
		t.Fatal(err)
	}
	blocked, err := second.commander.Start(context.Background(), secondManifest, second.startInput())
	if err != nil {
		t.Fatal(err)
	}
	if second.dispatcher.count() != 0 || blocked.Stages["second"].Status != state.StageQueued {
		t.Fatalf("cross-root overlap admitted: stage=%#v dispatches=%d", blocked.Stages["second"], second.dispatcher.count())
	}
}

func TestConflictingCrossRootAdoptionCannotIntegrate(t *testing.T) {
	authority, err := state.OpenAuthorityAt(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	first := newFixture(t)
	second := newFixture(t)
	first.commander.authority = authority
	second.commander.authority = authority
	firstManifest := first.manifest(implementationStage("first", "internal/shared", "shared-contract"))
	firstManifest.Spec.Repositories[0].Path = repository
	if _, err := first.commander.Start(context.Background(), firstManifest, first.startInput()); err != nil {
		t.Fatal(err)
	}
	adopted := implementationStage("adopted", "internal/shared/child", "adopted-contract")
	adopted.AdoptFleet = "existing-adopted"
	secondManifest := second.manifest(adopted)
	secondManifest.Spec.Repositories[0].Path = repository
	second.fleets.put(adapter.FleetEvidence{
		FleetID: "existing-adopted", Repository: "synthetic-repo", Status: adapter.FleetDone,
		ResultDigest: strings.Repeat("e", 64), Worktree: "worktree-adopted",
		InitialSHA: strings.Repeat("a", 40), IntentRevision: second.intentRevision,
	})
	blocked, err := second.commander.Start(context.Background(), secondManifest, second.startInput())
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.Stages["adopted"].GlobalClaimConflict {
		t.Fatalf("adoption conflict not persisted: %#v", blocked.Stages["adopted"])
	}
	blocked, err = second.commander.Reconcile(context.Background(), blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.integrator.runCalls != 0 || second.dagr.terminalCalls != 0 || !blocked.Stages["adopted"].GlobalClaimConflict {
		t.Fatalf("conflicting adoption advanced: stage=%#v integration=%d dagr=%d", blocked.Stages["adopted"], second.integrator.runCalls, second.dagr.terminalCalls)
	}
}

func TestReservationRemainsPreparedWhileWaitingForDispatchLock(t *testing.T) {
	fixture := newFixture(t)
	lockEntered := make(chan struct{})
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- fixture.store.WithDispatchLock(context.Background(), func() error {
			close(lockEntered)
			<-releaseLock
			return nil
		})
	}()
	<-lockEntered
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	startDone := make(chan error, 1)
	go func() {
		_, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
		startDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		run, err := fixture.store.LoadRun("run-a")
		if err == nil && run.Stages["build-api"].Reservation != nil {
			if run.Stages["build-api"].Reservation.Phase != state.ReservationPrepared {
				t.Fatalf("reservation became uncertain before dispatch lock: %q", run.Stages["build-api"].Reservation.Phase)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start did not publish prepared reservation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(releaseLock)
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
}

func TestIntentChangeWhileWaitingForDispatchLockPreventsAdapterCall(t *testing.T) {
	fixture := newFixture(t)
	lockEntered := make(chan struct{})
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- fixture.store.WithDispatchLock(context.Background(), func() error {
			close(lockEntered)
			<-releaseLock
			return nil
		})
	}()
	<-lockEntered
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	type startResult struct {
		run *state.Run
		err error
	}
	startDone := make(chan startResult, 1)
	go func() {
		run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
		startDone <- startResult{run: run, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	var intentPath string
	for {
		run, err := fixture.store.LoadRun("run-a")
		if err == nil && run.Stages["build-api"].Reservation != nil {
			intentPath = run.IntentPath
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start did not reach prepared reservation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(intentPath, []byte("changed while waiting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(releaseLock)
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	result := <-startDone
	if result.err == nil || result.run != nil {
		t.Fatalf("tampered dispatch returned run=%#v err=%v", result.run, result.err)
	}
	if fixture.dispatcher.count() != 0 {
		t.Fatalf("tampered intent reached adapter %d times", fixture.dispatcher.count())
	}
}

func TestRestartRequeuesInterruptedIntegrationWithoutAssumingSuccess(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	fixture.integrator.failHead = 1
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); err == nil {
		t.Fatal("Reconcile() unexpectedly passed failed base verification")
	}

	lease, err := fixture.store.AcquireLease(fixture.leaseOptions)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := fixture.store.LoadRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted.Generation = lease.Generation()
	interrupted.MergeQueue["synthetic-repo"][0].Status = state.CandidateIntegrating
	interrupted.Stages["build-api"].Status = state.StageIntegrating
	if err := fixture.store.SaveRun(interrupted, lease); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.MergeQueue["synthetic-repo"][0].Status != state.CandidateQueued || fixture.integrator.runCalls != 0 {
		t.Fatalf("restart trusted interrupted integration: queue=%#v calls=%d", recovered.MergeQueue, fixture.integrator.runCalls)
	}
	completed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != state.RunCompleted || fixture.integrator.runCalls != 1 {
		t.Fatalf("requeued integration did not complete: status=%q calls=%d", completed.Status, fixture.integrator.runCalls)
	}
}

func TestDrainStopsNewAdmissionsAndResumeRestartsThem(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(
		implementationStage("first", "internal/first", "first-contract"),
		implementationStage("second", "internal/second", "second-contract"),
	)
	m.Spec.Limits.Implementation = 1
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commander.SetDrained(context.Background(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	active := dispatchedStage(run)
	fixture.fleets.setStatus("fleet-"+active, adapter.FleetDone)
	drained, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.dispatcher.count() != 1 || drained.Status != state.RunDrained {
		t.Fatalf("drained run dispatched work: count=%d status=%q", fixture.dispatcher.count(), drained.Status)
	}
	if _, err := fixture.commander.SetDrained(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.commander.Reconcile(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if fixture.dispatcher.count() != 2 {
		t.Fatalf("resume dispatch count = %d, want 2", fixture.dispatcher.count())
	}
}

func TestDrainResumePreservesReconciliationBlocker(t *testing.T) {
	fixture := newFixture(t)
	m := fixture.manifest(implementationStage("build-api", "internal/api", "api-contract"))
	fixture.dispatcher.failAfterCreate = true
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err == nil || run.Status != state.RunReconcileRequired {
		t.Fatalf("uncertain start status=%q error=%v", run.Status, err)
	}
	if _, err := fixture.commander.SetDrained(context.Background(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	resumed, err := fixture.commander.SetDrained(context.Background(), run.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != state.RunReconcileRequired {
		t.Fatalf("resume erased reconciliation blocker: status=%q", resumed.Status)
	}
}

func TestFakeEndToEnd(t *testing.T) {
	fixture := newFixture(t)
	build := implementationStage("build-api", "internal/api", "api-contract")
	guide := implementationStage("write-guide", "docs/guide", "operator-guide")
	guide.DependsOn = []string{"build-api"}
	m := fixture.manifest(build, guide)

	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatalf("start fake run: %v", err)
	}
	if fixture.dispatcher.count() != 1 {
		t.Fatalf("initial dispatch count = %d", fixture.dispatcher.count())
	}
	fixture.fleets.setStatus("fleet-build-api", adapter.FleetDone)
	run, err = fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("reconcile build stage: %v", err)
	}
	if fixture.dispatcher.count() != 2 || run.Stages["write-guide"].Status != state.StageDispatched {
		t.Fatalf("dependent stage was not admitted: %#v", run.Stages)
	}
	fixture.fleets.setStatus("fleet-write-guide", adapter.FleetDone)
	run, err = fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("reconcile guide stage: %v", err)
	}
	if run.Status != state.RunCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	if fixture.dagr.terminalCalls != 2 || fixture.integrator.runCalls != 2 {
		t.Fatalf("terminal evidence dagr=%d integration=%d", fixture.dagr.terminalCalls, fixture.integrator.runCalls)
	}
	t.Logf("fake run %s completed with %d child fleets and no merge or push", run.ID, fixture.dispatcher.count())
}

func TestFailedDependencyMakesUnstartedDescendantsTerminal(t *testing.T) {
	fixture := newFixture(t)
	first := implementationStage("first", "internal/first", "first-contract")
	second := implementationStage("second", "internal/second", "second-contract")
	second.DependsOn = []string{"first"}
	m := fixture.manifest(first, second)
	run, err := fixture.commander.Start(context.Background(), m, fixture.startInput())
	if err != nil {
		t.Fatal(err)
	}
	fixture.fleets.setStatus("fleet-first", adapter.FleetFailed)
	failed, err := fixture.commander.Reconcile(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != state.RunFailed || failed.Stages["second"].Status != state.StageFailed {
		t.Fatalf("failed dependency did not converge run: %#v", failed)
	}
	if fixture.dispatcher.count() != 1 {
		t.Fatalf("unreachable descendant dispatched: %d", fixture.dispatcher.count())
	}
}

type fixture struct {
	t              *testing.T
	commander      *Commander
	dagr           *fakeDagr
	dispatcher     *fakeDispatcher
	fleets         *fakeFleets
	diff           *fakeDiff
	integrator     *fakeIntegrator
	intentPath     string
	intentRevision string
	manifestPath   string
	store          *state.Store
	leaseOptions   state.LeaseOptions
	lastManifest   *manifest.Manifest
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(root, "intent.md")
	writeTestFile(t, intentPath, "# Synthetic intent\n")
	manifestPath := filepath.Join(root, "platoon.yaml")
	writeTestFile(t, manifestPath, "synthetic manifest source\n")
	intentRevision, err := FileSHA256(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	fleets := newFakeFleets()
	dagr := newFakeDagr()
	dispatcher := &fakeDispatcher{fleets: fleets, intentRevision: intentRevision}
	diff := &fakeDiff{paths: map[string][]adapter.ChangedPath{}}
	integrator := &fakeIntegrator{head: strings.Repeat("d", 40), containsBase: true}
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	leaseOptions := state.LeaseOptions{
		TTL: time.Minute, Now: func() time.Time { return clock }, Hostname: "host-a", PID: 501,
		OwnerAlive: func(string, int) (bool, error) { return false, nil },
	}
	commander := New(store, Dependencies{
		Dagr: dagr, Dispatcher: dispatcher, Fleets: fleets, Diff: diff, Integrator: integrator,
		Now: func() time.Time { return clock }, ID: func() (string, error) { return "run-a", nil },
		Lease: leaseOptions,
	})
	return &fixture{
		t: t, commander: commander, dagr: dagr, dispatcher: dispatcher, fleets: fleets,
		diff: diff, integrator: integrator, intentPath: intentPath, intentRevision: intentRevision, manifestPath: manifestPath,
		store: store, leaseOptions: leaseOptions,
	}
}

func (f *fixture) manifest(stages ...manifest.Stage) *manifest.Manifest {
	f.t.Helper()
	m := &manifest.Manifest{
		APIVersion: manifest.APIVersion,
		Kind:       manifest.Kind,
		Metadata:   manifest.Metadata{Name: "synthetic-platoon"},
		Spec: manifest.Spec{
			Project: "synthetic-project", Mission: "docs/mission.md", Intent: "docs/intent.md",
			Limits: manifest.Limits{Implementation: 6, Review: 2, LeaseTTL: "1m", CommandTimeout: "1m", MaxOutputBytes: 65536},
			Adapters: manifest.Adapters{
				Dagr: manifest.DagrAdapter{Executable: "dagr", Database: "dagr.db", InspectExecutable: "sqlite3"},
				Sergeant: manifest.SergeantAdapter{
					FleetRoot: "fleets", OriginProfile: "platoon-local",
					Dispatch: manifest.Command{Executable: "sgt-dispatch"}, Watch: manifest.Command{Executable: "sgt-watch"},
					Wake: manifest.Command{Executable: "sgt-wake"}, Drain: manifest.Command{Executable: "sgt-drain"},
				},
			},
			Routing: []manifest.Route{{Model: "reasoning", Risk: "high", Harness: "opencode"}},
			Repositories: []manifest.Repository{{
				ID: "synthetic-repo", Path: "repos/synthetic-repo", Branch: "feat/synthetic-work", MaxWriters: 4,
				Integration: []manifest.Command{{Executable: "go", Args: []string{"test", "./..."}}},
			}},
			Stages: stages,
		},
	}
	if err := manifest.Validate(m); err != nil {
		f.t.Fatalf("test manifest invalid: %v", err)
	}
	f.lastManifest = m
	f.dagr.setManifest(m)
	return m
}

func (f *fixture) startInput() StartInput {
	if f.lastManifest == nil {
		f.t.Fatal("test manifest must be created before start input")
	}
	raw, err := yaml.Marshal(f.lastManifest)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.manifestPath, raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return StartInput{ManifestPath: f.manifestPath, ManifestBytes: raw, IntentPath: f.intentPath}
}

func implementationStage(id, pathClaim, semantic string) manifest.Stage {
	return manifest.Stage{
		ID: id, Repository: "synthetic-repo", Task: "task-" + id, Mode: manifest.Implementation,
		Harness: "opencode", Model: "reasoning", Risk: "high", DependsOn: []string{},
		Claims:     manifest.Claims{Paths: []string{pathClaim}, Semantic: []string{semantic}},
		Acceptance: []manifest.Command{{Executable: "go", Args: []string{"test", "./..."}}},
	}
}

type fakeDagr struct {
	mu                sync.Mutex
	stages            map[string]adapter.DagrStatus
	stageIDs          map[string]string
	dependencies      map[string][]string
	terminalCalls     int
	failTerminal      int
	failAfterTerminal int
	startCalls        int
	failLoad          int
	failList          int
	afterStart        func()
}

func newFakeDagr() *fakeDagr {
	return &fakeDagr{}
}

func (f *fakeDagr) LoadWorkflow(_ context.Context, _ string, _ string, names []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stages = map[string]adapter.DagrStatus{}
	f.stageIDs = map[string]string{}
	for index, name := range names {
		f.stageIDs[name] = fmt.Sprintf("%08d-0000-4000-8000-%012d", index+1, index+1)
		if len(f.dependencies[name]) == 0 {
			f.stages[name] = adapter.DagrReady
		} else {
			f.stages[name] = adapter.DagrPending
		}
	}
	if f.failLoad > 0 {
		f.failLoad--
		return errors.New("synthetic lost workflow receipt")
	}
	return nil
}

func (f *fakeDagr) ListStages(_ context.Context, _ string, _ []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList > 0 {
		f.failList--
		return nil, errors.New("synthetic lost stage-list receipt")
	}
	return cloneStringMap(f.stageIDs), nil
}

func (f *fakeDagr) StartRun(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	f.startCalls++
	afterStart := f.afterStart
	f.mu.Unlock()
	if afterStart != nil {
		afterStart()
	}
	return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil
}

func (f *fakeDagr) RecoverRun(_ context.Context, _ string) (adapter.DagrRecovery, error) {
	return adapter.DagrRecovery{State: adapter.DagrRunAbsent}, nil
}

func (f *fakeDagr) Snapshot(_ context.Context, _ string, _ []string) (map[string]adapter.DagrStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneStatusMap(f.stages), nil
}

func (f *fakeDagr) SetTerminal(_ context.Context, _ string, stageID string, success bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTerminal > 0 {
		f.failTerminal--
		return errors.New("synthetic dagr uncertainty")
	}
	name := ""
	for candidate, id := range f.stageIDs {
		if id == stageID {
			name = candidate
		}
	}
	if name == "" {
		return errors.New("unknown fake stage")
	}
	f.terminalCalls++
	if success {
		f.stages[name] = adapter.DagrDone
	} else {
		f.stages[name] = adapter.DagrFailed
	}
	if f.failAfterTerminal > 0 {
		f.failAfterTerminal--
		return errors.New("synthetic lost terminal acknowledgement")
	}
	for candidate, dependencies := range f.dependencies {
		if f.stages[candidate] != adapter.DagrPending {
			continue
		}
		ready := true
		for _, dependency := range dependencies {
			if f.stages[dependency] != adapter.DagrDone {
				ready = false
			}
		}
		if ready {
			f.stages[candidate] = adapter.DagrReady
		}
	}
	return nil
}

func (f *fakeDagr) setManifest(m *manifest.Manifest) {
	f.dependencies = map[string][]string{}
	for _, stage := range m.Spec.Stages {
		f.dependencies[stage.ID] = append([]string(nil), stage.DependsOn...)
	}
}

func (f *fakeDagr) resetStartCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = 0
}

func (f *fakeDagr) startCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCalls
}

type fakeDispatcher struct {
	mu               sync.Mutex
	fleets           *fakeFleets
	intentRevision   string
	requests         []adapter.DispatchRequest
	failAfterCreate  bool
	wrongCorrelation bool
	failBeforeCreate int
}

func (f *fakeDispatcher) Dispatch(_ context.Context, request adapter.DispatchRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.failBeforeCreate > 0 {
		f.failBeforeCreate--
		return "", errors.New("synthetic pre-child dispatch failure")
	}
	fleetID := "fleet-" + request.Stage
	f.fleets.put(adapter.FleetEvidence{
		FleetID: fleetID, Repository: request.Repository, Status: adapter.FleetInProgress,
		Worktree: "worktree-" + request.Stage, InitialSHA: strings.Repeat("a", 40), IntentRevision: f.intentRevision,
	})
	correlation := request.CorrelationID
	if f.wrongCorrelation {
		correlation = "different-correlation"
	}
	f.fleets.correlate(request.OriginProfile, correlation, fleetID)
	if f.failAfterCreate {
		return "", errors.New("synthetic lost receipt")
	}
	return fleetID, nil
}

func (f *fakeDispatcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeDispatcher) branches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		result = append(result, request.Branch)
	}
	sort.Strings(result)
	return result
}

func (f *fakeDispatcher) intentFiles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		result = append(result, request.IntentFile)
	}
	return result
}

func (f *fakeDispatcher) reset() {
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
	f.fleets.reset()
}

type fakeFleets struct {
	mu           sync.Mutex
	evidence     map[string]adapter.FleetEvidence
	correlations map[string][]string
}

func newFakeFleets() *fakeFleets {
	return &fakeFleets{evidence: map[string]adapter.FleetEvidence{}, correlations: map[string][]string{}}
}

func (f *fakeFleets) Read(fleetID, repository string, binding adapter.FleetBinding) (adapter.FleetEvidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	evidence, ok := f.evidence[fleetID]
	if !ok || evidence.Repository != repository || evidence.IntentRevision != binding.IntentRevision {
		return adapter.FleetEvidence{}, errors.New("unverified fake fleet")
	}
	if binding.RequireCorrelation {
		matches := f.correlations[binding.OriginProfile+"\x00"+binding.CorrelationID]
		if len(matches) != 1 || matches[0] != fleetID {
			return adapter.FleetEvidence{}, errors.New("unverified fake correlation")
		}
	}
	if (binding.Worktree != "" && binding.Worktree != evidence.Worktree) ||
		(binding.WorktreeGitPointer != "" && binding.WorktreeGitPointer != evidence.WorktreeGitPointer) ||
		(binding.WorktreeGitDir != "" && binding.WorktreeGitDir != evidence.WorktreeGitDir) ||
		(binding.InitialSHA != "" && binding.InitialSHA != evidence.InitialSHA) ||
		(binding.WorktreeIdentity != "" && binding.WorktreeIdentity != evidence.WorktreeIdentity) ||
		(binding.GitDirIdentity != "" && binding.GitDirIdentity != evidence.GitDirIdentity) {
		return adapter.FleetEvidence{}, errors.New("unverified fake Git identity")
	}
	return evidence, nil
}

func (f *fakeFleets) FindByCorrelation(profile, correlation string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.correlations[profile+"\x00"+correlation]...), nil
}

func (f *fakeFleets) put(evidence adapter.FleetEvidence) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if evidence.WorktreeGitPointer == "" {
		evidence.WorktreeGitPointer = "gitdir: git-dir-" + evidence.FleetID
	}
	if evidence.WorktreeGitDir == "" {
		evidence.WorktreeGitDir = "git-dir-" + evidence.FleetID
	}
	if evidence.WorktreeIdentity == "" {
		evidence.WorktreeIdentity = "worktree-identity-" + evidence.FleetID
	}
	if evidence.GitDirIdentity == "" {
		evidence.GitDirIdentity = "git-identity-" + evidence.FleetID
	}
	f.evidence[evidence.FleetID] = evidence
}

func (f *fakeFleets) correlate(profile, correlation, fleet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := profile + "\x00" + correlation
	f.correlations[key] = append(f.correlations[key], fleet)
	sort.Strings(f.correlations[key])
}

func (f *fakeFleets) setStatus(fleet string, status adapter.FleetStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	evidence := f.evidence[fleet]
	evidence.Status = status
	if status == adapter.FleetDone {
		evidence.ResultDigest = strings.Repeat("e", 64)
	}
	f.evidence[fleet] = evidence
}

func (f *fakeFleets) status(fleet string) adapter.FleetStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.evidence[fleet].Status
}

func (f *fakeFleets) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evidence = map[string]adapter.FleetEvidence{}
	f.correlations = map[string][]string{}
}

type fakeDiff struct {
	paths map[string][]adapter.ChangedPath
}

func (f *fakeDiff) ChangedPaths(_ context.Context, worktree, _, _ string) ([]adapter.ChangedPath, error) {
	return append([]adapter.ChangedPath(nil), f.paths[worktree]...), nil
}

type fakeIntegrator struct {
	mu           sync.Mutex
	head         string
	heads        []string
	headIndex    int
	runCalls     int
	mergeCalls   int
	pushCalls    int
	failHead     int
	entered      chan struct{}
	release      chan struct{}
	enterOnce    sync.Once
	containsBase bool
	afterRun     func()
	runError     error
}

func (f *fakeIntegrator) Head(_ context.Context, _ manifest.Repository) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failHead > 0 {
		f.failHead--
		return "", errors.New("synthetic base uncertainty")
	}
	if f.headIndex < len(f.heads) {
		head := f.heads[f.headIndex]
		f.headIndex++
		return head, nil
	}
	return f.head, nil
}

func (f *fakeIntegrator) Run(_ context.Context, _, _, _ string, _ []manifest.Command) error {
	f.mu.Lock()
	f.runCalls++
	entered := f.entered
	release := f.release
	f.mu.Unlock()
	if entered != nil {
		f.enterOnce.Do(func() { close(entered) })
	}
	if release != nil {
		<-release
	}
	if f.afterRun != nil {
		f.afterRun()
	}
	return f.runError
}

func (f *fakeIntegrator) ContainsBase(_ context.Context, _, _, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containsBase, nil
}

func candidateStatuses(run *state.Run, repository string) []state.CandidateStatus {
	result := make([]state.CandidateStatus, 0, len(run.MergeQueue[repository]))
	for _, candidate := range run.MergeQueue[repository] {
		result = append(result, candidate.Status)
	}
	return result
}

func dispatchedStage(run *state.Run) string {
	for id, stage := range run.Stages {
		if stage.Status == state.StageDispatched {
			return id
		}
	}
	return ""
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStatusMap(source map[string]adapter.DagrStatus) map[string]adapter.DagrStatus {
	result := make(map[string]adapter.DagrStatus, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
