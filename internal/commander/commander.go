package commander

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/adapter"
	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/planner"
	"github.com/mrtnebrle/platoon/internal/state"
)

const maxSourceFile = 4 << 20

type FleetEvidenceReader interface {
	Read(string, string, adapter.FleetBinding) (adapter.FleetEvidence, error)
	FindByCorrelation(string, string) ([]string, error)
}

type DiffInspector interface {
	ChangedPaths(context.Context, string, string, string) ([]adapter.ChangedPath, error)
}

type Integrator interface {
	Head(context.Context, manifest.Repository) (string, error)
	ContainsBase(context.Context, string, string, string) (bool, error)
	Run(context.Context, string, string, string, []manifest.Command) error
}

type Dependencies struct {
	Dagr         adapter.Dagr
	Dispatcher   adapter.Dispatcher
	Fleets       FleetEvidenceReader
	Diff         DiffInspector
	Integrator   Integrator
	Now          func() time.Time
	ID           func() (string, error)
	Lease        state.LeaseOptions
	DispatchLock string
	Authority    *state.Authority
}

type Commander struct {
	store        *state.Store
	dagr         adapter.Dagr
	dispatcher   adapter.Dispatcher
	fleets       FleetEvidenceReader
	diff         DiffInspector
	integrator   Integrator
	now          func() time.Time
	id           func() (string, error)
	lease        state.LeaseOptions
	dispatchLock string
	authority    *state.Authority
}

type StartInput struct {
	ManifestPath  string
	ManifestBytes []byte
	IntentPath    string
}

func New(store *state.Store, dependencies Dependencies) *Commander {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	id := dependencies.ID
	if id == nil {
		id = randomRunID
	}
	return &Commander{
		store: store, dagr: dependencies.Dagr, dispatcher: dependencies.Dispatcher,
		fleets: dependencies.Fleets, diff: dependencies.Diff, integrator: dependencies.Integrator,
		now: now, id: id, lease: dependencies.Lease,
		dispatchLock: dependencies.DispatchLock,
		authority:    dependencies.Authority,
	}
}

func (c *Commander) Start(ctx context.Context, m *manifest.Manifest, input StartInput) (run *state.Run, returnErr error) {
	if c.store == nil || c.dagr == nil || c.dispatcher == nil || c.fleets == nil || c.diff == nil || c.integrator == nil {
		return nil, errors.New("Commander dependencies are incomplete")
	}
	if m == nil {
		return nil, errors.New("manifest is required")
	}
	if len(input.ManifestBytes) == 0 {
		return nil, errors.New("validated manifest byte snapshot is required")
	}
	parsedManifest, err := manifest.Load(input.ManifestBytes)
	if err != nil {
		return nil, err
	}
	parsedComparable, err := normalizedManifest(parsedManifest)
	if err != nil {
		return nil, err
	}
	providedComparable, err := normalizedManifest(m)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(parsedComparable, providedComparable) {
		return nil, errors.New("manifest object does not match validated byte snapshot")
	}
	runtimeManifest, err := manifest.ResolveRuntimePaths(parsedManifest, input.ManifestPath)
	if err != nil {
		return nil, err
	}
	m = runtimeManifest
	manifestDigest, err := valueSHA256(m)
	if err != nil {
		return nil, err
	}
	manifestSourceSum := sha256.Sum256(input.ManifestBytes)
	manifestSourceDigest := hex.EncodeToString(manifestSourceSum[:])
	intentBytes, intentRevision, err := readSourceFile(input.IntentPath)
	if err != nil {
		return nil, fmt.Errorf("intent source is not a bounded regular file: %w", err)
	}
	lease, err := c.store.AcquireLease(c.lease)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := lease.Release(); returnErr == nil && err != nil {
			returnErr = err
		}
		if returnErr != nil && run != nil {
			durable, loadErr := c.store.LoadRun(run.ID)
			if loadErr != nil {
				run = nil
				returnErr = errors.Join(returnErr, loadErr)
			} else {
				run = durable
			}
		}
	}()

	runID, err := c.id()
	if err != nil {
		return nil, fmt.Errorf("generate run ID: %w", err)
	}
	runDirectory, err := c.store.RunDir(runID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(runDirectory); err == nil {
		return nil, errors.New("generated run ID already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	snapshot, err := cloneManifest(m)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	run = &state.Run{
		Version: state.StateVersion, ID: runID, Name: m.Metadata.Name,
		ManifestDigest: manifestDigest, ManifestSourceDigest: manifestSourceDigest,
		ManifestPath: input.ManifestPath, IntentPath: filepath.Join(runDirectory, "intent.md"),
		IntentRevision: intentRevision, Manifest: *snapshot, Generation: lease.Generation(),
		Status: state.RunInitialized, CreatedAt: now, UpdatedAt: now,
		Dagr:   state.DagrState{Workflow: "platoon-" + runID, Stages: map[string]string{}, Phase: "starting"},
		Stages: map[string]*state.StageState{}, MergeQueue: map[string][]*state.MergeCandidate{}, Blockers: []state.Blocker{},
	}
	for _, stage := range m.Spec.Stages {
		run.Stages[stage.ID] = &state.StageState{ID: stage.ID, Status: state.StagePending}
	}
	intentPath, err := c.store.WriteRunFile(run.ID, "intent.md", intentBytes, lease)
	if err != nil {
		return run, err
	}
	if intentPath != run.IntentPath {
		return run, errors.New("run-owned intent path changed unexpectedly")
	}
	if err := c.verifyProvenance(run); err != nil {
		return run, err
	}
	workflow, err := adapter.WorkflowYAML(run.Dagr.Workflow, m.Spec.Stages)
	if err != nil {
		return run, err
	}
	workflowPath, err := c.store.WriteRunFile(run.ID, "workflow.yaml", workflow, lease)
	if err != nil {
		return run, err
	}
	stageNames := sortedStageNames(m)
	run.Dagr.Phase = "loading_workflow"
	if err := c.save(run, lease); err != nil {
		return run, err
	}
	if err := c.prepareDagr(ctx, run, lease, workflowPath, stageNames); err != nil {
		return run, err
	}
	if err := c.startPreparedDagrRun(ctx, run, lease); err != nil {
		return run, err
	}

	for _, stage := range m.Spec.Stages {
		if stage.AdoptFleet == "" {
			continue
		}
		evidence, err := c.fleets.Read(stage.AdoptFleet, stage.Repository, c.binding(run, stage))
		if err != nil {
			markReconcileRequired(run)
			run.Stages[stage.ID].Status = state.StageBlocked
			run.Stages[stage.ID].Blocker = "adoption evidence is missing or ambiguous"
			addBlocker(run, stage.ID, "adoption_unverified", run.Stages[stage.ID].Blocker)
			return run, errors.Join(fmt.Errorf("adopt stage %q: %w", stage.ID, err), c.save(run, lease))
		}
		c.commitAdoption(run, stage, evidence, lease.Generation())
		if err := c.reserveGlobalClaim(ctx, run, stage, run.Stages[stage.ID]); err != nil {
			if errors.Is(err, state.ErrGlobalClaimConflict) {
				markReconcileRequired(run)
				run.Stages[stage.ID].GlobalClaimConflict = true
				run.Stages[stage.ID].Status = state.StageReconcileRequired
				run.Stages[stage.ID].Blocker = "adopted fleet conflicts with global repository claims"
				addBlocker(run, stage.ID, "global_claim_conflict", run.Stages[stage.ID].Blocker)
				continue
			}
			return run, err
		}
	}
	if c.adoptedPolicyConflict(run) {
		markReconcileRequired(run)
		if err := c.save(run, lease); err != nil {
			return run, err
		}
		return run, nil
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}

	dagrSnapshot, err := c.dagr.Snapshot(ctx, run.Dagr.RunID, stageNames)
	if err != nil {
		markReconcileRequired(run)
		addBlocker(run, "", "dagr_snapshot_unverified", "dagr readiness could not be verified")
		return run, errors.Join(err, c.save(run, lease))
	}
	c.applySnapshot(run, dagrSnapshot)
	if err := c.admitReady(ctx, run, lease, dagrSnapshot); err != nil {
		return run, err
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}
	return run, nil
}

func (c *Commander) prepareDagr(ctx context.Context, run *state.Run, lease *state.Lease, workflowPath string, stageNames []string) error {
	if run.Dagr.Phase == "loading_workflow" {
		if err := c.dagr.LoadWorkflow(ctx, workflowPath, run.Dagr.Workflow, stageNames); err != nil {
			markReconcileRequired(run)
			addBlocker(run, "", "dagr_workflow_uncertain", "dagr workflow load requires durable verification")
			return errors.Join(err, c.save(run, lease))
		}
		run.Dagr.Phase = "workflow_loaded"
		clearBlocker(run, "", "dagr_workflow_uncertain")
		if err := c.save(run, lease); err != nil {
			return err
		}
	}
	if run.Dagr.Phase != "workflow_loaded" {
		return errors.New("dagr workflow is not ready for stage discovery")
	}
	stageIDs, err := c.dagr.ListStages(ctx, run.Dagr.Workflow, stageNames)
	if err != nil {
		markReconcileRequired(run)
		addBlocker(run, "", "dagr_stages_uncertain", "dagr stage identities require durable verification")
		return errors.Join(err, c.save(run, lease))
	}
	run.Dagr.Stages = stageIDs
	run.Dagr.Phase = "prepared"
	clearBlocker(run, "", "dagr_stages_uncertain")
	return c.save(run, lease)
}

func (c *Commander) startPreparedDagrRun(ctx context.Context, run *state.Run, lease *state.Lease) error {
	if run.Dagr.Phase != "prepared" {
		return errors.New("dagr run is not prepared")
	}
	run.Dagr.Phase = "starting_run"
	if err := c.save(run, lease); err != nil {
		return err
	}
	runID, err := c.dagr.StartRun(ctx, run.Dagr.Workflow)
	if err != nil {
		markReconcileRequired(run)
		addBlocker(run, "", "dagr_start_uncertain", "dagr run start outcome requires durable recovery")
		return errors.Join(err, c.save(run, lease))
	}
	run.Dagr.RunID = runID
	run.Dagr.Phase = "active"
	run.Status = state.RunActive
	clearBlocker(run, "", "dagr_start_uncertain")
	return c.save(run, lease)
}

func (c *Commander) Status(runID string) (*state.Run, error) {
	return c.store.LoadRun(runID)
}

func (c *Commander) SetDrained(ctx context.Context, runID string, drained bool) (run *state.Run, returnErr error) {
	_ = ctx
	lease, err := c.store.AcquireLease(c.lease)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := lease.Release(); returnErr == nil && err != nil {
			returnErr = err
		}
		if returnErr != nil && run != nil {
			durable, loadErr := c.store.LoadRun(run.ID)
			if loadErr != nil {
				run = nil
				returnErr = errors.Join(returnErr, loadErr)
			} else {
				run = durable
			}
		}
	}()
	run, err = c.store.LoadRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Status == state.RunCompleted || run.Status == state.RunFailed {
		return run, errors.New("terminal run cannot be drained or resumed")
	}
	run.Generation = lease.Generation()
	if drained {
		if run.Status == state.RunDrained {
			if err := c.save(run, lease); err != nil {
				return run, err
			}
			return run, nil
		}
		run.PreDrainStatus = run.Status
		run.Status = state.RunDrained
	} else {
		if run.Status != state.RunDrained {
			return run, errors.New("run is not drained")
		}
		run.Status = run.PreDrainStatus
		run.PreDrainStatus = ""
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}
	return run, nil
}

func (c *Commander) admitReady(ctx context.Context, run *state.Run, lease *state.Lease, snapshot map[string]adapter.DagrStatus) error {
	active := c.activeClaims(run)
	for _, priority := range planner.Priorities(&run.Manifest) {
		if snapshot[priority.Stage] != adapter.DagrReady {
			continue
		}
		stage, _ := run.Manifest.Stage(priority.Stage)
		stageState := run.Stages[stage.ID]
		if stageState.FleetID != "" || (stageState.Reservation != nil && stageState.Reservation.Phase != state.ReservationReleased) {
			continue
		}
		admitted, reason := planner.CanAdmit(&run.Manifest, stage, active)
		if !admitted {
			stageState.Status = state.StageQueued
			stageState.Blocker = reason
			continue
		}
		if err := c.verifyProvenance(run); err != nil {
			markReconcileRequired(run)
			stageState.Status = state.StageBlocked
			stageState.Blocker = "run provenance changed before dispatch"
			addBlocker(run, stage.ID, "provenance_mismatch", stageState.Blocker)
			return errors.Join(err, c.save(run, lease))
		}
		if err := c.reserveGlobalClaim(ctx, run, stage, stageState); err != nil {
			if errors.Is(err, state.ErrGlobalClaimConflict) {
				stageState.Status = state.StageQueued
				stageState.Blocker = "global repository claim conflict"
				continue
			}
			return err
		}
		correlation := run.ID + "-" + stage.ID
		stageState.Status = state.StageReserved
		stageState.Blocker = ""
		stageState.Reservation = &state.Reservation{
			Phase: state.ReservationPrepared, Generation: lease.Generation(), CorrelationID: correlation, ReservedAt: c.now().UTC(),
		}
		if err := c.save(run, lease); err != nil {
			_ = c.releaseGlobalClaim(ctx, stageState)
			return err
		}
		if err := c.dispatchPreparedStage(ctx, run, lease, stage, stageState); err != nil {
			return err
		}
		active = append(active, planner.ActiveClaim{
			Stage: stage.ID, Repository: stage.Repository, Mode: stage.Mode,
			Paths: append([]string(nil), stage.Claims.Paths...), Semantic: append([]string(nil), stage.Claims.Semantic...),
		})
	}
	return nil
}

func (c *Commander) reserveGlobalClaim(ctx context.Context, run *state.Run, configured manifest.Stage, stage *state.StageState) error {
	if c.authority == nil {
		return nil
	}
	repository, _ := run.Manifest.Repository(configured.Repository)
	repositoryKey, err := state.RepositoryKey(repository.Path)
	if err != nil {
		return err
	}
	claimID, reserveErr := c.authority.ReserveClaim(ctx, state.GlobalClaim{
		RepositoryKey: repositoryKey, Mode: configured.Mode,
		Paths: append([]string(nil), configured.Claims.Paths...), Semantic: append([]string(nil), configured.Claims.Semantic...),
		MaxWriters: repository.MaxWriters, StateRoot: c.store.Root(), RunID: run.ID, StageID: configured.ID, Adopted: stage.Adopted,
	})
	if claimID != "" {
		stage.GlobalClaimID = claimID
		stage.RepositoryKey = repositoryKey
	}
	if reserveErr == nil {
		stage.GlobalClaimConflict = false
	}
	return reserveErr
}

func (c *Commander) releaseGlobalClaim(ctx context.Context, stage *state.StageState) error {
	if c.authority == nil || stage == nil || stage.GlobalClaimID == "" {
		return nil
	}
	if err := c.authority.ReleaseClaim(ctx, stage.GlobalClaimID); err != nil {
		return err
	}
	stage.GlobalClaimID = ""
	return nil
}

func (c *Commander) dispatchPreparedStage(ctx context.Context, run *state.Run, lease *state.Lease, stage manifest.Stage, stageState *state.StageState) error {
	if stageState.Reservation == nil || stageState.Reservation.Phase != state.ReservationPrepared {
		return errors.New("stage does not have a prepared reservation")
	}
	if err := c.verifyProvenance(run); err != nil {
		return err
	}
	repository, _ := run.Manifest.Repository(stage.Repository)
	correlation := stageState.Reservation.CorrelationID
	request := adapter.DispatchRequest{
		Project: run.Manifest.Spec.Project, Task: stage.Task, Repository: stage.Repository,
		Branch: childBranch(repository.Branch, stage.ID), Harness: stage.Harness, Stage: stage.ID,
		IntentFile: run.IntentPath, OriginProfile: run.Manifest.Spec.Adapters.Sergeant.OriginProfile, CorrelationID: correlation,
	}
	var fleetID string
	dispatchStarted := false
	dispatchOperation := func() error {
		if err := c.verifyProvenance(run); err != nil {
			return err
		}
		stageState.Reservation.Phase = state.ReservationDispatching
		stageState.Reservation.DispatchAttempts++
		if err := c.save(run, lease); err != nil {
			stageState.Reservation.Phase = state.ReservationPrepared
			stageState.Reservation.DispatchAttempts--
			return err
		}
		dispatchStarted = true
		var err error
		fleetID, err = c.dispatcher.Dispatch(ctx, request)
		return err
	}
	var dispatchErr error
	if c.authority != nil {
		dispatchErr = c.authority.WithDispatchLock(ctx, dispatchOperation)
	} else if c.dispatchLock == "" {
		dispatchErr = c.store.WithDispatchLock(ctx, dispatchOperation)
	} else {
		dispatchErr = c.store.WithDispatchLockAt(ctx, c.dispatchLock, dispatchOperation)
	}
	if dispatchErr != nil {
		if !dispatchStarted {
			return dispatchErr
		}
		stageState.Status = state.StageReconcileRequired
		stageState.Blocker = "dispatch outcome requires correlation reconciliation"
		stageState.Reservation.Phase = state.ReservationReconcileRequired
		markReconcileRequired(run)
		addBlocker(run, stage.ID, "dispatch_uncertain", stageState.Blocker)
		return errors.Join(dispatchErr, c.save(run, lease))
	}
	stageState.Reservation.FleetID = fleetID
	evidence, evidenceErr := c.fleets.Read(fleetID, stage.Repository, c.correlatedBinding(run, stage, correlation))
	if evidenceErr != nil {
		stageState.Status = state.StageReconcileRequired
		stageState.Blocker = "dispatch receipt did not match durable fleet evidence"
		stageState.Reservation.Phase = state.ReservationReconcileRequired
		markReconcileRequired(run)
		addBlocker(run, stage.ID, "dispatch_unverified", stageState.Blocker)
		return errors.Join(evidenceErr, c.save(run, lease))
	}
	stageState.FleetID = evidence.FleetID
	pinFleetEvidence(stageState, evidence)
	stageState.Status = state.StageDispatched
	stageState.Reservation.Phase = state.ReservationCommitted
	stageState.Reservation.FleetID = evidence.FleetID
	return c.save(run, lease)
}

func (c *Commander) activeClaims(run *state.Run) []planner.ActiveClaim {
	result := make([]planner.ActiveClaim, 0, len(run.Stages))
	for _, configured := range run.Manifest.Spec.Stages {
		stage := run.Stages[configured.ID]
		if stage == nil || stage.Reservation == nil {
			continue
		}
		tokenReleased := stage.Reservation.Phase == state.ReservationReleased || stage.Reservation.Phase == state.ReservationAbsent
		claimsReleased := stage.Status == state.StageDone || stage.Status == state.StageFailed || stage.Reservation.Phase == state.ReservationAbsent
		result = append(result, planner.ActiveClaim{
			Stage: configured.ID, Repository: configured.Repository, Mode: configured.Mode,
			Paths: append([]string(nil), configured.Claims.Paths...), Semantic: append([]string(nil), configured.Claims.Semantic...),
			TokenReleased: tokenReleased, ClaimsReleased: claimsReleased,
		})
	}
	return result
}

func (c *Commander) adoptedPolicyConflict(run *state.Run) bool {
	clearBlocker(run, "", "adoption_capacity_conflict")
	for stageID := range run.Stages {
		clearBlocker(run, stageID, "adoption_writer_conflict")
		clearBlocker(run, stageID, "adoption_claim_conflict")
	}
	active := c.activeClaims(run)
	implementation, review := 0, 0
	conflict := false
	for _, claim := range active {
		if claim.TokenReleased {
			continue
		}
		if claim.Mode == manifest.Review {
			review++
		} else {
			implementation++
		}
	}
	if implementation > run.Manifest.Spec.Limits.Implementation || review > run.Manifest.Spec.Limits.Review {
		addBlocker(run, "", "adoption_capacity_conflict", "adopted fleets exceed configured token capacity")
		conflict = true
	}
	for index, left := range active {
		if left.ClaimsReleased || left.Mode != manifest.Implementation {
			continue
		}
		repository, _ := run.Manifest.Repository(left.Repository)
		writers := 0
		for _, candidate := range active {
			if candidate.Repository == left.Repository && candidate.Mode == manifest.Implementation && !candidate.ClaimsReleased {
				writers++
			}
		}
		if writers > repository.MaxWriters {
			addBlocker(run, left.Stage, "adoption_writer_conflict", "adopted fleets exceed repository writer policy")
			conflict = true
		}
		for otherIndex := index + 1; otherIndex < len(active); otherIndex++ {
			if overlaps, reason := planner.ActiveClaimsConflict(active[otherIndex], left); overlaps {
				addBlocker(run, active[otherIndex].Stage, "adoption_claim_conflict", reason)
				conflict = true
			}
		}
	}
	return conflict
}

func (c *Commander) applySnapshot(run *state.Run, snapshot map[string]adapter.DagrStatus) {
	for stageID, dagrStatus := range snapshot {
		stage := run.Stages[stageID]
		if stage == nil || stage.FleetID != "" {
			continue
		}
		switch dagrStatus {
		case adapter.DagrReady:
			if stage.Status == state.StagePending || stage.Status == state.StageQueued || stage.Status == state.StageReady {
				stage.Status = state.StageReady
			}
		case adapter.DagrSkipped:
			stage.Status = state.StageFailed
			stage.Blocker = "dagr skipped stage after dependency failure"
			addBlocker(run, stageID, "dagr_skipped", stage.Blocker)
		}
	}
}

func (c *Commander) commitAdoption(run *state.Run, configured manifest.Stage, evidence adapter.FleetEvidence, generation uint64) {
	stage := run.Stages[configured.ID]
	stage.FleetID = evidence.FleetID
	pinFleetEvidence(stage, evidence)
	stage.Adopted = true
	stage.Status = stageStatusFromFleet(evidence.Status)
	stage.Reservation = &state.Reservation{
		Phase: state.ReservationCommitted, Generation: generation, FleetID: evidence.FleetID, ReservedAt: c.now().UTC(),
	}
}

func (c *Commander) binding(run *state.Run, stage manifest.Stage) adapter.FleetBinding {
	repository, _ := run.Manifest.Repository(stage.Repository)
	return adapter.FleetBinding{
		Project: run.Manifest.Spec.Project, Task: stage.Task, Stage: stage.ID,
		Branch: childBranch(repository.Branch, stage.ID), IntentRevision: run.IntentRevision,
	}
}

func (c *Commander) correlatedBinding(run *state.Run, stage manifest.Stage, correlation string) adapter.FleetBinding {
	binding := c.binding(run, stage)
	binding.RequireCorrelation = true
	binding.OriginProfile = run.Manifest.Spec.Adapters.Sergeant.OriginProfile
	binding.CorrelationID = correlation
	return binding
}

func (c *Commander) bindingForStage(run *state.Run, configured manifest.Stage, stage *state.StageState) adapter.FleetBinding {
	if stage != nil && !stage.Adopted && stage.Reservation != nil && stage.Reservation.CorrelationID != "" {
		binding := c.correlatedBinding(run, configured, stage.Reservation.CorrelationID)
		return withPinnedFleetBinding(binding, stage)
	}
	return withPinnedFleetBinding(c.binding(run, configured), stage)
}

func pinFleetEvidence(stage *state.StageState, evidence adapter.FleetEvidence) {
	stage.Worktree = evidence.Worktree
	stage.WorktreeGitPointer = evidence.WorktreeGitPointer
	stage.WorktreeGitDir = evidence.WorktreeGitDir
	stage.InitialSHA = evidence.InitialSHA
	stage.WorktreeIdentity = evidence.WorktreeIdentity
	stage.GitDirIdentity = evidence.GitDirIdentity
}

func withPinnedFleetBinding(binding adapter.FleetBinding, stage *state.StageState) adapter.FleetBinding {
	if stage == nil {
		return binding
	}
	binding.Worktree = stage.Worktree
	binding.WorktreeGitPointer = stage.WorktreeGitPointer
	binding.WorktreeGitDir = stage.WorktreeGitDir
	binding.InitialSHA = stage.InitialSHA
	binding.WorktreeIdentity = stage.WorktreeIdentity
	binding.GitDirIdentity = stage.GitDirIdentity
	return binding
}

func (c *Commander) save(run *state.Run, lease *state.Lease) error {
	run.Generation = lease.Generation()
	run.UpdatedAt = c.now().UTC()
	return c.store.SaveRun(run, lease)
}

func (c *Commander) verifyProvenance(run *state.Run) error {
	runDirectory, err := c.store.RunDir(run.ID)
	if err != nil {
		return err
	}
	expectedIntent := filepath.Join(runDirectory, "intent.md")
	if filepath.Clean(run.IntentPath) != expectedIntent {
		return errors.New("run intent is not the run-owned snapshot")
	}
	manifestDigest, err := valueSHA256(run.Manifest)
	if err != nil || manifestDigest != run.ManifestDigest {
		return errors.New("persisted manifest digest does not match")
	}
	intentRevision, err := FileSHA256(run.IntentPath)
	if err != nil || intentRevision != run.IntentRevision {
		return errors.New("run-owned intent digest does not match")
	}
	return nil
}

func sortedStageNames(m *manifest.Manifest) []string {
	result := make([]string, 0, len(m.Spec.Stages))
	for _, stage := range m.Spec.Stages {
		result = append(result, stage.ID)
	}
	sort.Strings(result)
	return result
}

func prioritizedStages(m *manifest.Manifest) []manifest.Stage {
	result := make([]manifest.Stage, 0, len(m.Spec.Stages))
	for _, priority := range planner.Priorities(m) {
		stage, _ := m.Stage(priority.Stage)
		result = append(result, stage)
	}
	return result
}

func stageStatusFromFleet(status adapter.FleetStatus) state.StageStatus {
	switch status {
	case adapter.FleetInProgress:
		return state.StageInProgress
	case adapter.FleetNeedsInput:
		return state.StageNeedsInput
	case adapter.FleetBlocked, adapter.FleetOrphaned:
		return state.StageBlocked
	case adapter.FleetWaiting, adapter.FleetDrained:
		return state.StageWaiting
	default:
		return state.StageDispatched
	}
}

func addBlocker(run *state.Run, stage, code, message string) {
	for index := range run.Blockers {
		if run.Blockers[index].Stage == stage && run.Blockers[index].Code == code {
			run.Blockers[index].Message = message
			return
		}
	}
	run.Blockers = append(run.Blockers, state.Blocker{Stage: stage, Code: code, Message: message})
	sort.Slice(run.Blockers, func(i, j int) bool {
		if run.Blockers[i].Stage != run.Blockers[j].Stage {
			return run.Blockers[i].Stage < run.Blockers[j].Stage
		}
		return run.Blockers[i].Code < run.Blockers[j].Code
	})
}

func clearBlocker(run *state.Run, stage, code string) {
	filtered := run.Blockers[:0]
	for _, blocker := range run.Blockers {
		if blocker.Stage != stage || blocker.Code != code {
			filtered = append(filtered, blocker)
		}
	}
	run.Blockers = filtered
}

func markReconcileRequired(run *state.Run) {
	if run.Status == state.RunDrained {
		run.PreDrainStatus = state.RunReconcileRequired
		return
	}
	run.Status = state.RunReconcileRequired
}

func childBranch(base, stageID string) string {
	return strings.TrimSuffix(base, "-") + "-" + stageID
}

func FileSHA256(path string) (string, error) {
	_, digest, err := readSourceFile(path)
	return digest, err
}

func readSourceFile(path string) ([]byte, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxSourceFile {
		return nil, "", errors.New("source must be a bounded regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, "", errors.New("source changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSourceFile+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxSourceFile {
		return nil, "", errors.New("source exceeded its size limit while reading")
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func valueSHA256(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneManifest(source *manifest.Manifest) (*manifest.Manifest, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var destination manifest.Manifest
	if err := json.Unmarshal(raw, &destination); err != nil {
		return nil, err
	}
	return &destination, nil
}

func normalizedManifest(source *manifest.Manifest) (*manifest.Manifest, error) {
	result, err := cloneManifest(source)
	if err != nil {
		return nil, err
	}
	normalizeCommand := func(command *manifest.Command) {
		if command.Args == nil {
			command.Args = []string{}
		}
	}
	if result.Spec.Adapters.Dagr.Args == nil {
		result.Spec.Adapters.Dagr.Args = []string{}
	}
	for _, command := range []*manifest.Command{
		&result.Spec.Adapters.Sergeant.Dispatch, &result.Spec.Adapters.Sergeant.Watch,
		&result.Spec.Adapters.Sergeant.Wake, &result.Spec.Adapters.Sergeant.Drain,
	} {
		normalizeCommand(command)
	}
	for repositoryIndex := range result.Spec.Repositories {
		repository := &result.Spec.Repositories[repositoryIndex]
		if repository.Integration == nil {
			repository.Integration = []manifest.Command{}
		}
		for commandIndex := range repository.Integration {
			normalizeCommand(&repository.Integration[commandIndex])
		}
	}
	for stageIndex := range result.Spec.Stages {
		stage := &result.Spec.Stages[stageIndex]
		if stage.DependsOn == nil {
			stage.DependsOn = []string{}
		}
		if stage.Claims.Paths == nil {
			stage.Claims.Paths = []string{}
		}
		if stage.Claims.Semantic == nil {
			stage.Claims.Semantic = []string{}
		}
		if stage.Acceptance == nil {
			stage.Acceptance = []manifest.Command{}
		}
		for commandIndex := range stage.Acceptance {
			normalizeCommand(&stage.Acceptance[commandIndex])
		}
	}
	return result, nil
}

func randomRunID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "run-" + hex.EncodeToString(raw), nil
}

func sanitizeViolationMessage(violations []adapter.PathViolation) string {
	paths := make([]string, 0, len(violations))
	for index, violation := range violations {
		if index == 10 {
			paths = append(paths, fmt.Sprintf("and-%d-more", len(violations)-index))
			break
		}
		paths = append(paths, violation.Path)
	}
	return "out-of-claim paths: " + strings.Join(paths, ", ")
}
