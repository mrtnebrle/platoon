package missioncontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestTypedRunStorePublishesVerifiedEffectDisabledGenesis(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	bundleBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := CompileForApply(m, manifestPath, bundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Ready || preview.Packet == nil {
		t.Fatalf("compiled preview is not ready: %#v", preview)
	}

	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	published, err := store.PublishGenesis(GenesisInput{
		RunID:       "synthetic-run",
		Packet:      *preview.Packet,
		PublishedAt: time.Date(2026, 8, 8, 10, 2, 0, 0, time.UTC),
		Rollback:    testRollbackEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.State.Status != TypedRunInitializing || published.State.EffectsEnabled {
		t.Fatalf("published state = %#v", published.State)
	}
	if published.Fence.RepairEpoch != 0 || published.Fence.Generation != 1 || published.Fence.TransitionDigest == "" {
		t.Fatalf("published fence = %#v", published.Fence)
	}
	if published.State.PacketDigest != preview.Packet.ID || published.Previous != nil {
		t.Fatalf("published binding = %#v", published)
	}

	loaded, err := store.Load("synthetic-run")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fence != published.Fence || loaded.State.ResultingStateDigest != published.State.ResultingStateDigest {
		t.Fatalf("loaded snapshot = %#v, want %#v", loaded, published)
	}
	if len(loaded.State.ObservationDigests) != len(bundle.Observations) || loaded.State.ProjectionDigest == "" || loaded.State.EventDigest == "" {
		t.Fatalf("loaded object bindings = %#v", loaded.State)
	}
}

func TestTypedRunGenesisRetriesEveryPublicationBoundary(t *testing.T) {
	for _, boundary := range []PublicationBoundary{
		BoundaryPacketPublished,
		BoundaryPacketSynced,
		BoundaryObservationPublished,
		BoundaryObservationSynced,
		BoundaryProjectionPublished,
		BoundaryProjectionSynced,
		BoundaryEventPublished,
		BoundaryEventSynced,
		BoundaryTransitionPublished,
		BoundaryTransitionSynced,
		BoundaryBeforePointerPublish,
		BoundaryAfterPointerPublish,
		BoundaryAfterPointerSync,
	} {
		t.Run(string(boundary), func(t *testing.T) {
			input := testGenesisInput(t, "retry-run")
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			injected := false
			store.failpoint = func(current PublicationBoundary) error {
				if !injected && current == boundary {
					injected = true
					return errors.New("synthetic crash")
				}
				return nil
			}
			if _, err := store.PublishGenesis(input); err == nil {
				t.Fatal("publication succeeded across injected crash")
			}
			if !injected {
				t.Fatal("requested publication boundary was not reached")
			}
			if current, err := store.Load(input.RunID); err == nil {
				if boundary != BoundaryAfterPointerPublish && boundary != BoundaryAfterPointerSync {
					t.Fatalf("boundary %s unexpectedly published authority: %#v", boundary, current)
				}
			} else if boundary == BoundaryAfterPointerPublish || boundary == BoundaryAfterPointerSync {
				t.Fatalf("boundary %s left a partial pointer: %v", boundary, err)
			}

			store.failpoint = nil
			first, err := store.PublishGenesis(input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.PublishGenesis(input)
			if err != nil {
				t.Fatal(err)
			}
			if first.Fence != second.Fence {
				t.Fatalf("exact retry changed authority: %#v != %#v", first.Fence, second.Fence)
			}
		})
	}
}

func TestTypedRunGenesisRequiresRollbackEvidenceBeforeCreatingRunFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := openTestTypedRunStore(t, root)
	input := testGenesisInput(t, "no-rollback")
	input.Rollback = RollbackEvidence{}
	if _, err := store.PublishGenesis(input); err == nil || !strings.Contains(err.Error(), "rollback evidence") {
		t.Fatalf("missing rollback evidence error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "typed-runs", input.RunID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing rollback evidence created run files: %v", err)
	}
}

func TestTypedRunGenesisRejectsWellFormedUntrustedRollbackEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := openTestTypedRunStore(t, root)
	input := testGenesisInput(t, "untrusted-rollback")
	input.Rollback.ArtifactDigest = strings.Repeat("c", 64)
	if _, err := store.PublishGenesis(input); err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("untrusted rollback error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "typed-runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted rollback evidence created state: %v", err)
	}
}

func TestTypedRunGenesisRequiresTrustedRollbackVerifier(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := OpenTypedRunStore(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishGenesis(testGenesisInput(t, "no-verifier")); err == nil || !strings.Contains(err.Error(), "trusted rollback verifier") {
		t.Fatalf("missing rollback verifier error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing rollback verifier created state: %v", err)
	}
}

func TestTypedRunGenesisRejectsInvalidRunIDsBeforeCreatingFiles(t *testing.T) {
	for _, runID := range []string{".", "with:colon", "with/slash", "../escape", strings.Repeat("a", 129)} {
		t.Run(runID, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			store := openTestTypedRunStore(t, root)
			input := testGenesisInput(t, runID)
			if _, err := store.PublishGenesis(input); err == nil {
				t.Fatal("invalid run ID was accepted")
			}
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid run ID created state: %v", err)
			}
		})
	}
}

func TestTypedRunGenesisRejectsBundleProvenanceDivergence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := openTestTypedRunStore(t, root)
	input := testGenesisInput(t, "bundle-divergence")
	bundle := input.Packet.compiled.Bundle
	bundle.Observations[0].ObservedAt = "2026-08-08T10:00:01Z"
	rebuilt, err := NewSourceBundle(bundle.DeclarationDigest, bundle.SourceCatalogDigest, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	input.Packet.compiled.Bundle = rebuilt
	if _, err := store.PublishGenesis(input); err == nil || !strings.Contains(err.Error(), "observation set") {
		t.Fatalf("divergent bundle error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "typed-runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("divergent bundle created state: %v", err)
	}
}

func TestTypedObjectLimitsBoundEventsAndObservations(t *testing.T) {
	event := eventObject{Schema: eventSchema, EvidenceDigests: []string{strings.Repeat("a", maxEventObjectSize)}}
	if err := validateTypedObjectSize("events", event); err == nil {
		t.Fatal("oversized event was accepted")
	}
	observation := observationObject{Schema: observationObjectSchema, Observation: SourceObservation{Payload: map[string]any{"value": strings.Repeat("a", maxObservationObjectSize)}}}
	if err := validateTypedObjectSize("observations", observation); err == nil {
		t.Fatal("oversized observation was accepted")
	}
}

func TestTypedRunRecoveryQuarantinesInvalidCurrentBeforeReconcileRequired(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "repair-run"))
	if err != nil {
		t.Fatal(err)
	}
	invalidDigest := strings.Repeat("c", 64)
	invalidStateDigest := strings.Repeat("d", 64)
	invalid := runPointer{
		Schema: runPointerSchema, RunID: "repair-run", RepairEpoch: 0,
		Current: TransitionReference{Generation: 2, TransitionDigest: invalidDigest, ResultingStateDigest: invalidStateDigest},
		Previous: &TransitionReference{
			Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest,
			ResultingStateDigest: genesis.State.ResultingStateDigest,
		},
	}
	writeTestPointer(t, store.pointerPath("repair-run"), invalid)

	injected := false
	store.failpoint = func(boundary PublicationBoundary) error {
		if !injected && boundary == BoundaryAfterPointerSync {
			injected = true
			return errors.New("synthetic crash after quarantine")
		}
		return nil
	}
	if _, err := store.Recover("repair-run", TypedRunFence{RepairEpoch: 0, Generation: 2, TransitionDigest: invalidDigest}); err == nil {
		t.Fatal("recovery succeeded across injected quarantine boundary")
	}
	quarantine, err := store.Load("repair-run")
	if err != nil {
		t.Fatal(err)
	}
	if quarantine.Fence.RepairEpoch != 1 || quarantine.Fence.Generation != 3 || quarantine.State.Status != TypedRunQuarantined || quarantine.State.EffectsEnabled {
		t.Fatalf("quarantine snapshot = %#v", quarantine)
	}
	if quarantine.Previous == nil || quarantine.Previous.TransitionDigest != genesis.Fence.TransitionDigest {
		t.Fatalf("quarantine recovery base = %#v", quarantine.Previous)
	}

	store.failpoint = nil
	repaired, err := store.Recover("repair-run", quarantine.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Fence.RepairEpoch != 1 || repaired.Fence.Generation != 4 || repaired.State.Status != TypedRunReconcileRequired || repaired.State.EffectsEnabled {
		t.Fatalf("repaired snapshot = %#v", repaired)
	}
	if repaired.Previous == nil || repaired.Previous.Generation != 3 || repaired.Previous.TransitionDigest != quarantine.Fence.TransitionDigest {
		t.Fatalf("repaired predecessor = %#v", repaired.Previous)
	}

	stale := quarantine.Fence
	if _, err := store.Recover("repair-run", stale); !errors.Is(err, ErrTypedRunFenced) {
		t.Fatalf("stale recovery error = %v, want ErrTypedRunFenced", err)
	}
	current, err := store.Load("repair-run")
	if err != nil {
		t.Fatal(err)
	}
	if current.Fence != repaired.Fence {
		t.Fatalf("stale recovery changed authority: %#v", current.Fence)
	}
}

func TestTypedRunPublishesFencedEffectDisabledSuccessorAndRetriesExactly(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "successor-run"))
	if err != nil {
		t.Fatal(err)
	}
	input := SuccessorInput{
		RunID: "successor-run", Expected: genesis.Fence,
		PublishedAt: time.Date(2026, 8, 8, 10, 3, 0, 0, time.UTC),
	}
	first, err := store.PublishSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fence.RepairEpoch != genesis.Fence.RepairEpoch || first.Fence.Generation != genesis.Fence.Generation+1 ||
		first.Previous == nil || first.Previous.TransitionDigest != genesis.Fence.TransitionDigest || first.State.EffectsEnabled {
		t.Fatalf("successor snapshot = %#v", first)
	}
	second, err := store.PublishSuccessor(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fence != first.Fence {
		t.Fatalf("exact successor retry changed authority: %#v != %#v", second.Fence, first.Fence)
	}
	stale := input
	stale.Expected.TransitionDigest = strings.Repeat("e", 64)
	if _, err := store.PublishSuccessor(stale); !errors.Is(err, ErrTypedRunFenced) {
		t.Fatalf("stale successor error = %v", err)
	}
}

func TestTypedRunSuccessorRetriesEveryPublicationBoundary(t *testing.T) {
	for _, boundary := range []PublicationBoundary{
		BoundaryEventPublished, BoundaryEventSynced, BoundaryTransitionPublished, BoundaryTransitionSynced,
		BoundaryBeforePointerPublish, BoundaryAfterPointerPublish, BoundaryAfterPointerSync,
	} {
		t.Run(string(boundary), func(t *testing.T) {
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			genesis, err := store.PublishGenesis(testGenesisInput(t, "successor-retry"))
			if err != nil {
				t.Fatal(err)
			}
			input := SuccessorInput{
				RunID: "successor-retry", Expected: genesis.Fence,
				PublishedAt: time.Date(2026, 8, 8, 10, 3, 0, 0, time.UTC),
			}
			injected := false
			store.failpoint = func(current PublicationBoundary) error {
				if !injected && current == boundary {
					injected = true
					return errors.New("synthetic crash")
				}
				return nil
			}
			if _, err := store.PublishSuccessor(input); err == nil {
				t.Fatal("successor crossed injected crash")
			}
			store.failpoint = nil
			retried, err := store.PublishSuccessor(input)
			if err != nil {
				t.Fatal(err)
			}
			if retried.Fence.Generation != 2 || retried.State.EffectsEnabled {
				t.Fatalf("retried successor = %#v", retried)
			}
		})
	}
}

func TestTypedRunRecoveryUsesVerifiedNonGenesisPredecessor(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "later-repair"))
	if err != nil {
		t.Fatal(err)
	}
	successor, err := store.PublishSuccessor(SuccessorInput{
		RunID: "later-repair", Expected: genesis.Fence,
		PublishedAt: time.Date(2026, 8, 8, 10, 3, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := runPointer{
		Schema: runPointerSchema, RunID: "later-repair", RepairEpoch: successor.Fence.RepairEpoch,
		Current: TransitionReference{Generation: 3, TransitionDigest: strings.Repeat("c", 64), ResultingStateDigest: strings.Repeat("d", 64)},
		Previous: &TransitionReference{
			Generation: successor.Fence.Generation, TransitionDigest: successor.Fence.TransitionDigest,
			ResultingStateDigest: successor.State.ResultingStateDigest,
		},
	}
	writeTestPointer(t, store.pointerPath("later-repair"), invalid)
	repaired, err := store.Recover("later-repair", TypedRunFence{Generation: 3, TransitionDigest: invalid.Current.TransitionDigest})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Fence.RepairEpoch != 1 || repaired.Fence.Generation != 5 || repaired.State.Status != TypedRunReconcileRequired || repaired.State.EffectsEnabled {
		t.Fatalf("later repair snapshot = %#v", repaired)
	}
	invalidAgain := runPointer{
		Schema: runPointerSchema, RunID: "later-repair", RepairEpoch: repaired.Fence.RepairEpoch,
		Current: TransitionReference{Generation: 6, TransitionDigest: strings.Repeat("e", 64), ResultingStateDigest: strings.Repeat("f", 64)},
		Previous: &TransitionReference{
			Generation: repaired.Fence.Generation, TransitionDigest: repaired.Fence.TransitionDigest,
			ResultingStateDigest: repaired.State.ResultingStateDigest,
		},
	}
	writeTestPointer(t, store.pointerPath("later-repair"), invalidAgain)
	repairedAgain, err := store.Recover("later-repair", TypedRunFence{RepairEpoch: 1, Generation: 6, TransitionDigest: invalidAgain.Current.TransitionDigest})
	if err != nil {
		t.Fatal(err)
	}
	if repairedAgain.Fence.RepairEpoch != 2 || repairedAgain.Fence.Generation != 8 || repairedAgain.State.Status != TypedRunReconcileRequired {
		t.Fatalf("repeated later repair snapshot = %#v", repairedAgain)
	}
}

func TestTypedRunRecoveryExactOriginalFenceRetriesEveryBoundary(t *testing.T) {
	tests := []struct {
		boundary   PublicationBoundary
		occurrence int
	}{
		{BoundaryEventPublished, 1}, {BoundaryEventSynced, 1}, {BoundaryTransitionPublished, 1}, {BoundaryTransitionSynced, 1},
		{BoundaryBeforePointerPublish, 1}, {BoundaryAfterPointerPublish, 1}, {BoundaryAfterPointerSync, 1},
		{BoundaryEventPublished, 2}, {BoundaryEventSynced, 2}, {BoundaryTransitionPublished, 2}, {BoundaryTransitionSynced, 2},
		{BoundaryBeforePointerPublish, 2}, {BoundaryAfterPointerPublish, 2}, {BoundaryAfterPointerSync, 2},
	}
	for _, test := range tests {
		name := fmt.Sprintf("%s-%d", test.boundary, test.occurrence)
		t.Run(name, func(t *testing.T) {
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			genesis, err := store.PublishGenesis(testGenesisInput(t, "recovery-retry"))
			if err != nil {
				t.Fatal(err)
			}
			invalid := runPointer{
				Schema: runPointerSchema, RunID: "recovery-retry",
				Current: TransitionReference{Generation: 2, TransitionDigest: strings.Repeat("c", 64), ResultingStateDigest: strings.Repeat("d", 64)},
				Previous: &TransitionReference{
					Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest,
					ResultingStateDigest: genesis.State.ResultingStateDigest,
				},
			}
			writeTestPointer(t, store.pointerPath("recovery-retry"), invalid)
			expected := TypedRunFence{Generation: 2, TransitionDigest: invalid.Current.TransitionDigest}
			seen := 0
			store.failpoint = func(boundary PublicationBoundary) error {
				if boundary == test.boundary {
					seen++
					if seen == test.occurrence {
						return errors.New("synthetic crash")
					}
				}
				return nil
			}
			if _, err := store.Recover("recovery-retry", expected); err == nil {
				t.Fatal("recovery crossed injected crash")
			}
			store.failpoint = nil
			repaired, err := store.Recover("recovery-retry", expected)
			if err != nil {
				t.Fatal(err)
			}
			if repaired.Fence.Generation != 4 || repaired.Fence.RepairEpoch != 1 || repaired.State.Status != TypedRunReconcileRequired {
				t.Fatalf("retried recovery = %#v", repaired)
			}
		})
	}
}

func TestTypedRunGenesisRejectsUnsafeProjectionBeforeWritingObjects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store := openTestTypedRunStore(t, root)
	input := testGenesisInput(t, "unsafe-run")
	input.Packet.compiled.Bundle.Observations[0].Revision = "/private/synthetic/source"
	if _, err := store.PublishGenesis(input); err == nil {
		t.Fatalf("unsafe projection error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "typed-runs", input.RunID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe projection created run files: %v", err)
	}
}

func TestTypedRunRejectsConflictingRetryAndEveryStaleFenceComponent(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	input := testGenesisInput(t, "fenced-run")
	genesis, err := store.PublishGenesis(input)
	if err != nil {
		t.Fatal(err)
	}
	conflict := input
	conflict.PublishedAt = conflict.PublishedAt.Add(time.Second)
	if _, err := store.PublishGenesis(conflict); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting retry error = %v", err)
	}

	for name, stale := range map[string]TypedRunFence{
		"epoch":      {RepairEpoch: genesis.Fence.RepairEpoch + 1, Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest},
		"generation": {RepairEpoch: genesis.Fence.RepairEpoch, Generation: genesis.Fence.Generation + 1, TransitionDigest: genesis.Fence.TransitionDigest},
		"digest":     {RepairEpoch: genesis.Fence.RepairEpoch, Generation: genesis.Fence.Generation, TransitionDigest: strings.Repeat("e", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Recover(input.RunID, stale); !errors.Is(err, ErrTypedRunFenced) {
				t.Fatalf("stale fence error = %v, want ErrTypedRunFenced", err)
			}
			current, err := store.Load(input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Fence != genesis.Fence {
				t.Fatalf("stale fence changed authority: %#v", current.Fence)
			}
		})
	}
}

func TestTypedRunRecoveryCASRejectsPointerChangeAtPublicationBoundary(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "cas-run"))
	if err != nil {
		t.Fatal(err)
	}
	base := TransitionReference{
		Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest,
		ResultingStateDigest: genesis.State.ResultingStateDigest,
	}
	invalid := runPointer{
		Schema: runPointerSchema, RunID: "cas-run",
		Current:  TransitionReference{Generation: 2, TransitionDigest: strings.Repeat("c", 64), ResultingStateDigest: strings.Repeat("d", 64)},
		Previous: &base,
	}
	writeTestPointer(t, store.pointerPath("cas-run"), invalid)
	competing := invalid
	competing.Current.TransitionDigest = strings.Repeat("e", 64)

	store.failpoint = func(boundary PublicationBoundary) error {
		if boundary == BoundaryBeforePointerPublish {
			writeTestPointer(t, store.pointerPath("cas-run"), competing)
		}
		return nil
	}
	_, err = store.Recover("cas-run", TypedRunFence{Generation: 2, TransitionDigest: invalid.Current.TransitionDigest})
	if !errors.Is(err, ErrTypedRunFenced) {
		t.Fatalf("pointer CAS error = %v, want ErrTypedRunFenced", err)
	}
	var retained runPointer
	if err := readStrictJSON(store.pointerPath("cas-run"), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Current != competing.Current {
		t.Fatalf("recovery overwrote competing pointer: %#v", retained.Current)
	}
}

func TestTypedRunRecoveryRejectsNonadjacentBaseAndCounterOverflowBeforeMutation(t *testing.T) {
	for name, pointer := range map[string]runPointer{
		"nonadjacent": {
			Schema: runPointerSchema, RunID: "preflight-run",
			Current: TransitionReference{Generation: 3, TransitionDigest: strings.Repeat("c", 64), ResultingStateDigest: strings.Repeat("d", 64)},
		},
		"epoch overflow": {
			Schema: runPointerSchema, RunID: "preflight-run", RepairEpoch: ^uint64(0),
			Current: TransitionReference{Generation: 2, TransitionDigest: strings.Repeat("e", 64), ResultingStateDigest: strings.Repeat("f", 64)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			genesis, err := store.PublishGenesis(testGenesisInput(t, "preflight-run"))
			if err != nil {
				t.Fatal(err)
			}
			candidate := pointer
			candidate.Previous = &TransitionReference{
				Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest,
				ResultingStateDigest: genesis.State.ResultingStateDigest,
			}
			writeTestPointer(t, store.pointerPath("preflight-run"), candidate)
			before, err := os.ReadFile(store.pointerPath("preflight-run"))
			if err != nil {
				t.Fatal(err)
			}
			expected := TypedRunFence{
				RepairEpoch: candidate.RepairEpoch, Generation: candidate.Current.Generation,
				TransitionDigest: candidate.Current.TransitionDigest,
			}
			if _, err := store.Recover("preflight-run", expected); err == nil {
				t.Fatal("invalid recovery preflight succeeded")
			}
			after, err := os.ReadFile(store.pointerPath("preflight-run"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed recovery preflight changed current pointer")
			}
		})
	}
	if _, err := checkedIncrement(^uint64(0)); err == nil {
		t.Fatal("counter overflow was accepted")
	}
}

func TestTypedRunRecoveryStateMustDeriveFromVerifiedBase(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "derived-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	invalid := runPointer{
		Schema: runPointerSchema, RunID: "derived-recovery",
		Current: TransitionReference{Generation: 2, TransitionDigest: strings.Repeat("c", 64), ResultingStateDigest: strings.Repeat("d", 64)},
		Previous: &TransitionReference{
			Generation: genesis.Fence.Generation, TransitionDigest: genesis.Fence.TransitionDigest,
			ResultingStateDigest: genesis.State.ResultingStateDigest,
		},
	}
	writeTestPointer(t, store.pointerPath("derived-recovery"), invalid)
	store.failpoint = func(boundary PublicationBoundary) error {
		if boundary == BoundaryAfterPointerSync {
			return errors.New("stop after quarantine")
		}
		return nil
	}
	_, _ = store.Recover("derived-recovery", TypedRunFence{Generation: 2, TransitionDigest: invalid.Current.TransitionDigest})
	store.failpoint = nil
	quarantineSnapshot, err := store.Load("derived-recovery")
	if err != nil {
		t.Fatal(err)
	}
	var quarantine transitionCommit
	if err := store.readObject("derived-recovery", "transitions", quarantineSnapshot.Fence.TransitionDigest, &quarantine); err != nil {
		t.Fatal(err)
	}
	quarantine.ResultingState.SourceBundle.QueryScope = "different-scope"
	quarantine.ResultingState.ResultingStateDigest, _ = digestState(quarantine.ResultingState)
	quarantine.Digest = ""
	quarantine.Digest, _ = digestWithoutField(transitionSchema, quarantine, func(value *transitionCommit) { value.Digest = "" })
	quarantinePointer := runPointer{
		Schema: runPointerSchema, RunID: "derived-recovery", RepairEpoch: quarantine.RepairEpoch,
		Current: TransitionReference{
			Generation: quarantine.Generation, TransitionDigest: quarantine.Digest,
			ResultingStateDigest: quarantine.ResultingState.ResultingStateDigest,
		},
		Previous: quarantine.RecoveryBase,
	}
	if err := store.validateQuarantineCommit("derived-recovery", quarantinePointer, quarantine); err == nil || !strings.Contains(err.Error(), "derive") {
		t.Fatalf("divergent quarantine error = %v", err)
	}

	repaired, err := store.Recover("derived-recovery", quarantineSnapshot.Fence)
	if err != nil {
		t.Fatal(err)
	}
	var repair transitionCommit
	if err := store.readObject("derived-recovery", "transitions", repaired.Fence.TransitionDigest, &repair); err != nil {
		t.Fatal(err)
	}
	repair.ResultingState.SourceBundle.QueryScope = "different-scope"
	repair.ResultingState.ResultingStateDigest, _ = digestState(repair.ResultingState)
	repair.Digest = ""
	repair.Digest, _ = digestWithoutField(transitionSchema, repair, func(value *transitionCommit) { value.Digest = "" })
	repairPointer := runPointer{
		Schema: runPointerSchema, RunID: "derived-recovery", RepairEpoch: repair.RepairEpoch,
		Current:  TransitionReference{Generation: repair.Generation, TransitionDigest: repair.Digest, ResultingStateDigest: repair.ResultingState.ResultingStateDigest},
		Previous: repaired.Previous,
	}
	if err := store.validateRepairCommit("derived-recovery", repairPointer, repair); err == nil || !strings.Contains(err.Error(), "derive") {
		t.Fatalf("divergent repair error = %v", err)
	}
}

func TestTypedRunLoadIgnoresUnreferencedPartialObjectsAndForks(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	genesis, err := store.PublishGenesis(testGenesisInput(t, "fork-run"))
	if err != nil {
		t.Fatal(err)
	}
	var current transitionCommit
	if err := store.readObject("fork-run", "transitions", genesis.Fence.TransitionDigest, &current); err != nil {
		t.Fatal(err)
	}
	writeOrphanGenesis(t, store, current, time.Date(2026, 8, 8, 10, 3, 0, 0, time.UTC))
	writeOrphanGenesis(t, store, current, time.Date(2026, 8, 8, 10, 4, 0, 0, time.UTC))
	partialPath := filepath.Join(store.runDir("fork-run"), "objects", "events", strings.Repeat("f", 64)+".json")
	if err := os.WriteFile(partialPath, []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("fork-run")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fence != genesis.Fence {
		t.Fatalf("unreferenced objects changed authority: %#v", loaded.Fence)
	}
}

func TestTypedRunLoadRejectsChangedMissingUnsupportedAndMalformedAuthority(t *testing.T) {
	tests := map[string]func(t *testing.T, store *TypedRunStore, published *TypedRunSnapshot){
		"changed observation bytes": func(t *testing.T, store *TypedRunStore, published *TypedRunSnapshot) {
			path := filepath.Join(store.runDir("invalid-run"), "objects", "observations", published.State.ObservationDigests[0]+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(raw), `"adapterVersion":"v1"`, `"adapterVersion":"v2"`, 1)
			if changed == string(raw) {
				t.Fatal("observation fixture did not contain adapter version")
			}
			if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"missing event": func(t *testing.T, store *TypedRunStore, published *TypedRunSnapshot) {
			path := filepath.Join(store.runDir("invalid-run"), "objects", "events", published.State.EventDigest+".json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"unsupported packet schema": func(t *testing.T, store *TypedRunStore, published *TypedRunSnapshot) {
			path := filepath.Join(store.runDir("invalid-run"), "objects", "packets", published.State.PacketDigest+".json")
			var packet packetObject
			if err := readStrictJSON(path, &packet); err != nil {
				t.Fatal(err)
			}
			packet.Schema = "platoon.packet/v9"
			writeTestObject(t, path, packet)
		},
		"malformed pointer": func(t *testing.T, store *TypedRunStore, _ *TypedRunSnapshot) {
			if err := os.WriteFile(store.pointerPath("invalid-run"), []byte(`{"schema":`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			published, err := store.PublishGenesis(testGenesisInput(t, "invalid-run"))
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, store, published)
			if _, err := store.Load("invalid-run"); err == nil {
				t.Fatal("invalid authority loaded successfully")
			}
		})
	}
}

func TestTypedRunLoadRejectsNoncanonicalImmutableObjectBytes(t *testing.T) {
	for _, kind := range []string{"packets", "observations", "projections", "events", "transitions"} {
		t.Run(kind, func(t *testing.T) {
			store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
			published, err := store.PublishGenesis(testGenesisInput(t, "canonical-run"))
			if err != nil {
				t.Fatal(err)
			}
			digest := map[string]string{
				"packets": published.State.PacketDigest, "observations": published.State.ObservationDigests[0],
				"projections": published.State.ProjectionDigest, "events": published.State.EventDigest,
				"transitions": published.Fence.TransitionDigest,
			}[kind]
			path := filepath.Join(store.runDir("canonical-run"), "objects", kind, digest+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append([]byte(" "), raw...), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load("canonical-run"); err == nil || !strings.Contains(err.Error(), "canonical") {
				t.Fatalf("noncanonical %s error = %v", kind, err)
			}
		})
	}

	t.Run("duplicate known key", func(t *testing.T) {
		store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
		published, err := store.PublishGenesis(testGenesisInput(t, "duplicate-run"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.runDir("duplicate-run"), "objects", "transitions", published.Fence.TransitionDigest+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		duplicate := append([]byte(`{"schema":"platoon.transition-commit/v1alpha1",`), raw[1:]...)
		if err := os.WriteFile(path, duplicate, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load("duplicate-run"); err == nil {
			t.Fatal("duplicate known key was accepted")
		}
	})
}

func TestProjectionPersistsClosedSourceLabelSeparateFromSourceID(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	published, err := store.PublishGenesis(testGenesisInput(t, "source-label-run"))
	if err != nil {
		t.Fatal(err)
	}
	var projection projectionObject
	if err := store.readObject("source-label-run", "projections", published.State.ProjectionDigest, &projection); err != nil {
		t.Fatal(err)
	}
	foundDistinct := false
	for _, entry := range projection.Entries {
		if !sourceKinds[entry.SourceLabel] {
			t.Fatalf("source label is not closed: %#v", entry)
		}
		foundDistinct = foundDistinct || entry.SourceLabel != entry.SourceID
	}
	if !foundDistinct {
		t.Fatal("projection source labels duplicate declaration IDs")
	}
}

func TestTypedRunRejectsConflictingImmutableTransitionBytes(t *testing.T) {
	store := openTestTypedRunStore(t, filepath.Join(t.TempDir(), "state"))
	published, err := store.PublishGenesis(testGenesisInput(t, "immutable-run"))
	if err != nil {
		t.Fatal(err)
	}
	var commit transitionCommit
	if err := store.readObject("immutable-run", "transitions", published.Fence.TransitionDigest, &commit); err != nil {
		t.Fatal(err)
	}
	commit.Reason = "conflicting-retry"
	if err := store.writeImmutable("immutable-run", "transitions", published.Fence.TransitionDigest, commit); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting immutable transition error = %v", err)
	}
	current, err := store.Load("immutable-run")
	if err != nil {
		t.Fatal(err)
	}
	if current.Fence != published.Fence {
		t.Fatalf("conflicting immutable write changed authority: %#v", current.Fence)
	}
}

func TestTypedRunGenesisRejectsSecretAndOversizedContentBeforeWriting(t *testing.T) {
	for name, mutate := range map[string]func(*GenesisInput){
		"secret": func(input *GenesisInput) {
			input.Packet.compiled.Bundle.Observations[0].Revision = "token=synthetic-private-value"
		},
		"oversized": func(input *GenesisInput) {
			input.Packet.compiled.Bundle.Observations[0].Payload["operations"] = []any{"ack", "list", "load", "start", "watch", strings.Repeat("x", maxTypedObjectSize+1)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			store := openTestTypedRunStore(t, root)
			input := testGenesisInput(t, "bounded-run")
			mutate(&input)
			if _, err := store.PublishGenesis(input); err == nil {
				t.Fatal("unsafe or oversized content was accepted")
			}
			if _, err := os.Lstat(filepath.Join(root, "typed-runs", input.RunID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected content created run files: %v", err)
			}
		})
	}
}

func writeOrphanGenesis(t *testing.T, store *TypedRunStore, current transitionCommit, occurredAt time.Time) {
	t.Helper()
	event := eventObject{
		Schema: eventSchema, RunID: current.RunID, Sequence: 1, RunGeneration: 1, ProjectionRevision: 0,
		OccurredAt: occurredAt.Format(time.RFC3339Nano), Type: "packet_run_published", Subject: current.RunID,
		EvidenceDigests: append([]string(nil), current.ObservationDigests...),
	}
	var err error
	event.Digest, err = digestWithoutField(eventSchema, event, func(value *eventObject) { value.Digest = "" })
	if err != nil {
		t.Fatal(err)
	}
	state := current.ResultingState
	state.EventDigest = event.Digest
	state.ResultingStateDigest, err = digestState(state)
	if err != nil {
		t.Fatal(err)
	}
	orphan := current
	orphan.EventDigest = event.Digest
	orphan.ResultingState = state
	orphan.Digest = ""
	orphan.Digest, err = digestWithoutField(transitionSchema, orphan, func(value *transitionCommit) { value.Digest = "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeImmutable(current.RunID, "events", event.Digest, event); err != nil {
		t.Fatal(err)
	}
	if err := store.writeImmutable(current.RunID, "transitions", orphan.Digest, orphan); err != nil {
		t.Fatal(err)
	}
}

func writeTestObject(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestPointer(t *testing.T, path string, pointer runPointer) {
	t.Helper()
	raw, err := canonicalJSON(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testGenesisInput(t *testing.T, runID string) GenesisInput {
	t.Helper()
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	bundleBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := CompileForApply(m, manifestPath, bundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Ready || preview.Packet == nil {
		t.Fatalf("compiled preview is not ready: %#v", preview)
	}
	return GenesisInput{
		RunID: runID, Packet: *preview.Packet,
		PublishedAt: time.Date(2026, 8, 8, 10, 2, 0, 0, time.UTC),
		Rollback:    testRollbackEvidence(),
	}
}

type exactRollbackVerifier struct {
	expected RollbackEvidence
}

func (v exactRollbackVerifier) VerifyRollback(evidence RollbackEvidence) error {
	if evidence != v.expected {
		return errors.New("unrecognized rollback evidence")
	}
	return nil
}

func testRollbackEvidence() RollbackEvidence {
	return RollbackEvidence{
		ArtifactVersion: "v0.1.0-rollback", ArtifactDigest: strings.Repeat("a", 64),
		FixtureDigest: strings.Repeat("b", 64), ReadableSchema: TypedRunSchema, CreationDisabled: true,
	}
}

func openTestTypedRunStore(t *testing.T, root string) *TypedRunStore {
	t.Helper()
	store, err := OpenTypedRunStore(root, exactRollbackVerifier{expected: testRollbackEvidence()})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
