package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/opaqueid"
)

const StateVersion = "platoon.state/v1alpha1"

type RunStatus string

const (
	RunInitialized       RunStatus = "initialized"
	RunActive            RunStatus = "active"
	RunDrained           RunStatus = "drained"
	RunReconcileRequired RunStatus = "reconcile_required"
	RunCompleted         RunStatus = "completed"
	RunFailed            RunStatus = "failed"
)

type StageStatus string

const (
	StagePending           StageStatus = "pending"
	StageReady             StageStatus = "ready"
	StageQueued            StageStatus = "queued"
	StageReserved          StageStatus = "reserved"
	StageDispatched        StageStatus = "dispatched"
	StageInProgress        StageStatus = "in_progress"
	StageWaiting           StageStatus = "waiting"
	StageNeedsInput        StageStatus = "needs_input"
	StageBlocked           StageStatus = "blocked"
	StageCandidate         StageStatus = "candidate"
	StageIntegrating       StageStatus = "integrating"
	StageMergeReady        StageStatus = "merge_ready"
	StageDone              StageStatus = "done"
	StageFailed            StageStatus = "failed"
	StageOutOfClaim        StageStatus = "out_of_claim"
	StageReconcileRequired StageStatus = "reconcile_required"
)

type ReservationPhase string

const (
	ReservationPrepared          ReservationPhase = "prepared"
	ReservationDispatching       ReservationPhase = "dispatching"
	ReservationCommitted         ReservationPhase = "committed"
	ReservationReconcileRequired ReservationPhase = "reconcile_required"
	ReservationAbsent            ReservationPhase = "absent"
	ReservationReleased          ReservationPhase = "released"
)

type CandidateStatus string

const (
	CandidateQueued      CandidateStatus = "queued"
	CandidateIntegrating CandidateStatus = "integrating"
	CandidateMergeReady  CandidateStatus = "merge_ready"
	CandidateBlocked     CandidateStatus = "blocked"
	CandidateFailed      CandidateStatus = "failed"
)

type Run struct {
	Version              string                       `json:"version"`
	ID                   string                       `json:"id"`
	Name                 string                       `json:"name"`
	ManifestDigest       string                       `json:"manifestDigest"`
	ManifestSourceDigest string                       `json:"manifestSourceDigest"`
	ManifestPath         string                       `json:"manifestPath"`
	IntentPath           string                       `json:"intentPath"`
	IntentRevision       string                       `json:"intentRevision"`
	Manifest             manifest.Manifest            `json:"manifest"`
	Generation           uint64                       `json:"generation"`
	Status               RunStatus                    `json:"status"`
	PreDrainStatus       RunStatus                    `json:"preDrainStatus"`
	CreatedAt            time.Time                    `json:"createdAt"`
	UpdatedAt            time.Time                    `json:"updatedAt"`
	Dagr                 DagrState                    `json:"dagr"`
	Stages               map[string]*StageState       `json:"stages"`
	MergeQueue           map[string][]*MergeCandidate `json:"mergeQueue"`
	Blockers             []Blocker                    `json:"blockers"`
}

type DagrState struct {
	Workflow string            `json:"workflow"`
	RunID    string            `json:"runId"`
	Stages   map[string]string `json:"stages"`
	Phase    string            `json:"phase"`
}

type StageState struct {
	ID                  string       `json:"id"`
	Status              StageStatus  `json:"status"`
	FleetID             string       `json:"fleetId"`
	Worktree            string       `json:"worktree"`
	WorktreeGitPointer  string       `json:"worktreeGitPointer"`
	WorktreeGitDir      string       `json:"worktreeGitDir"`
	InitialSHA          string       `json:"initialSha"`
	WorktreeIdentity    string       `json:"worktreeIdentity"`
	GitDirIdentity      string       `json:"gitDirIdentity"`
	GlobalClaimID       string       `json:"globalClaimId"`
	RepositoryKey       string       `json:"repositoryKey"`
	GlobalClaimConflict bool         `json:"globalClaimConflict"`
	Adopted             bool         `json:"adopted"`
	Reservation         *Reservation `json:"reservation"`
	DagrTerminalPending string       `json:"dagrTerminalPending"`
	FailureSource       string       `json:"failureSource"`
	Blocker             string       `json:"blocker"`
	Result              string       `json:"result"`
}

type Reservation struct {
	Phase            ReservationPhase `json:"phase"`
	Generation       uint64           `json:"generation"`
	CorrelationID    string           `json:"correlationId"`
	FleetID          string           `json:"fleetId"`
	DispatchAttempts int              `json:"dispatchAttempts"`
	ReservedAt       time.Time        `json:"reservedAt"`
	ReleasedAt       time.Time        `json:"releasedAt"`
}

type MergeCandidate struct {
	Stage              string          `json:"stage"`
	FleetID            string          `json:"fleetId"`
	Status             CandidateStatus `json:"status"`
	BaseSHA            string          `json:"baseSha"`
	Worktree           string          `json:"worktree"`
	WorktreeGitPointer string          `json:"worktreeGitPointer"`
	WorktreeGitDir     string          `json:"worktreeGitDir"`
	WorktreeIdentity   string          `json:"worktreeIdentity"`
	GitDirIdentity     string          `json:"gitDirIdentity"`
	InitialSHA         string          `json:"initialSha"`
	ResultDigest       string          `json:"resultDigest"`
	Generation         uint64          `json:"generation"`
	Attempts           int             `json:"attempts"`
	Diagnostic         string          `json:"diagnostic"`
}

type Blocker struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (r *Run) Validate() error {
	if r.Version != StateVersion {
		return fmt.Errorf("state version must be %q", StateVersion)
	}
	if !safeID(r.ID) {
		return errors.New("run id is invalid")
	}
	if r.Name == "" || r.Manifest.APIVersion != manifest.APIVersion || r.Manifest.Kind != manifest.Kind {
		return errors.New("run manifest identity is invalid")
	}
	if r.Generation == 0 {
		return errors.New("run generation must be positive")
	}
	switch r.Dagr.Phase {
	case "", "starting", "loading_workflow", "workflow_loaded", "prepared", "starting_run", "active", "reconcile_required":
	default:
		return errors.New("dagr phase is invalid")
	}
	if r.Dagr.Phase == "active" && !safeID(r.Dagr.RunID) {
		return errors.New("active dagr run identity is invalid")
	}
	switch r.Status {
	case RunInitialized, RunActive, RunDrained, RunReconcileRequired, RunCompleted, RunFailed:
	default:
		return fmt.Errorf("run status %q is invalid", r.Status)
	}
	if r.Status == RunDrained {
		if r.PreDrainStatus != RunActive && r.PreDrainStatus != RunReconcileRequired {
			return errors.New("drained run must retain its prior nonterminal status")
		}
	} else if r.PreDrainStatus != "" {
		return errors.New("non-drained run must not retain a prior drain status")
	}
	if r.Stages == nil || r.MergeQueue == nil {
		return errors.New("run stages and merge queue must be present")
	}
	configuredStages := make(map[string]manifest.Stage, len(r.Manifest.Spec.Stages))
	if err := manifest.Validate(&r.Manifest); err != nil {
		return fmt.Errorf("persisted manifest is invalid: %w", err)
	}
	manifestDigest, err := digestManifest(r.Manifest)
	if err != nil || r.ManifestDigest != manifestDigest || !validHex(r.ManifestSourceDigest, 64) ||
		!validHex(r.IntentRevision, 64) || r.ManifestPath == "" || r.IntentPath == "" {
		return errors.New("persisted manifest or intent provenance is invalid")
	}
	if len(r.Stages) != len(r.Manifest.Spec.Stages) {
		return errors.New("run stage state does not match the persisted manifest")
	}
	for _, configured := range r.Manifest.Spec.Stages {
		configuredStages[configured.ID] = configured
		if r.Stages[configured.ID] == nil {
			return fmt.Errorf("run is missing stage state for %q", configured.ID)
		}
		if r.Dagr.Phase == "active" && r.Dagr.Stages[configured.ID] == "" {
			return fmt.Errorf("run is missing dagr identity for %q", configured.ID)
		}
	}
	for id, stage := range r.Stages {
		if stage == nil || id != stage.ID || !safeID(id) {
			return errors.New("stage state identity is invalid")
		}
		configured, configuredExists := configuredStages[id]
		if !configuredExists {
			return fmt.Errorf("stage %q is not present in the persisted manifest", id)
		}
		if stage.Adopted {
			if configured.AdoptFleet == "" || stage.FleetID == "" || stage.FleetID != configured.AdoptFleet {
				return fmt.Errorf("stage %q adopted state does not match configured fleet", id)
			}
		} else if configured.AdoptFleet != "" && stage.FleetID != "" {
			return fmt.Errorf("stage %q configured adoption is not marked adopted", id)
		}
		if !validStageStatus(stage.Status) {
			return fmt.Errorf("stage %q status %q is invalid", id, stage.Status)
		}
		if stage.DagrTerminalPending != "" && stage.DagrTerminalPending != "done" && stage.DagrTerminalPending != "failed" {
			return fmt.Errorf("stage %q pending dagr terminal state is invalid", id)
		}
		if stage.FleetID != "" && !safeID(stage.FleetID) {
			return fmt.Errorf("stage %q fleet identity is invalid", id)
		}
		if stage.FleetID != "" && (stage.Worktree == "" || stage.WorktreeGitPointer == "" || stage.WorktreeGitDir == "" || stage.WorktreeIdentity == "" || stage.GitDirIdentity == "" ||
			(!validHex(stage.InitialSHA, 40) && !validHex(stage.InitialSHA, 64))) {
			return fmt.Errorf("stage %q pinned Git identity is invalid", id)
		}
		if stage.Result != "" && !validHex(stage.Result, 64) {
			return fmt.Errorf("stage %q result digest is invalid", id)
		}
		if !safeDiagnostic(stage.Blocker) {
			return fmt.Errorf("stage %q blocker is unsafe", id)
		}
		if stage.Reservation != nil {
			reservation := stage.Reservation
			if reservation.Generation == 0 || reservation.Generation > r.Generation {
				return fmt.Errorf("stage %q reservation generation is invalid", id)
			}
			if reservation.DispatchAttempts < 0 || reservation.DispatchAttempts > 2 {
				return fmt.Errorf("stage %q reservation dispatch attempts are invalid", id)
			}
			if reservation.Phase == ReservationDispatching && reservation.DispatchAttempts == 0 {
				return fmt.Errorf("stage %q dispatching reservation has no attempt", id)
			}
			switch reservation.Phase {
			case ReservationPrepared, ReservationDispatching, ReservationReconcileRequired:
				if reservation.CorrelationID == "" || !safeID(reservation.CorrelationID) {
					return fmt.Errorf("stage %q reservation correlation is invalid", id)
				}
			case ReservationCommitted, ReservationReleased:
				if !safeID(reservation.FleetID) {
					return fmt.Errorf("stage %q reservation fleet identity is invalid", id)
				}
			case ReservationAbsent:
				if reservation.FleetID != "" || reservation.DispatchAttempts != 2 {
					return fmt.Errorf("stage %q absent reservation evidence is invalid", id)
				}
			default:
				return fmt.Errorf("stage %q reservation phase is invalid", id)
			}
			if stage.FleetID != "" && reservation.FleetID != "" && reservation.FleetID != stage.FleetID {
				return fmt.Errorf("stage %q reservation does not match child identity", id)
			}
			if stage.Adopted {
				if reservation.CorrelationID != "" {
					return fmt.Errorf("stage %q adopted reservation has dispatch correlation", id)
				}
			} else if reservation.CorrelationID != r.ID+"-"+id {
				return fmt.Errorf("stage %q dispatched reservation correlation is invalid", id)
			}
		}
	}
	for repository, queue := range r.MergeQueue {
		if !safeID(repository) {
			return errors.New("merge queue repository identity is invalid")
		}
		seen := map[string]bool{}
		for _, candidate := range queue {
			if candidate == nil || !safeID(candidate.Stage) || !safeID(candidate.FleetID) || seen[candidate.Stage] ||
				candidate.Generation == 0 || candidate.Generation > r.Generation || candidate.Attempts < 0 {
				return fmt.Errorf("merge queue %q contains invalid candidate identity", repository)
			}
			seen[candidate.Stage] = true
			if configured, exists := configuredStages[candidate.Stage]; len(configuredStages) > 0 && (!exists || configured.Repository != repository) {
				return fmt.Errorf("merge queue %q candidate does not match its configured stage", repository)
			}
			stage := r.Stages[candidate.Stage]
			if stage == nil || stage.FleetID != candidate.FleetID || stage.Reservation == nil || stage.Reservation.FleetID != candidate.FleetID {
				return fmt.Errorf("merge queue %q candidate does not match reserved child", repository)
			}
			switch candidate.Status {
			case CandidateQueued, CandidateIntegrating, CandidateMergeReady, CandidateBlocked, CandidateFailed:
			default:
				return fmt.Errorf("merge queue %q contains invalid candidate status", repository)
			}
			if candidate.BaseSHA != "" && !validHex(candidate.BaseSHA, 40) && !validHex(candidate.BaseSHA, 64) {
				return fmt.Errorf("merge queue %q contains invalid base identity", repository)
			}
			if candidate.Worktree == "" || candidate.WorktreeGitPointer == "" || candidate.WorktreeGitDir == "" || candidate.WorktreeIdentity == "" || candidate.GitDirIdentity == "" || strings.ContainsAny(candidate.Worktree, "\x00\r\n") ||
				(!validHex(candidate.InitialSHA, 40) && !validHex(candidate.InitialSHA, 64)) || !validHex(candidate.ResultDigest, 64) {
				return fmt.Errorf("merge queue %q contains invalid terminal evidence", repository)
			}
			if !safeDiagnostic(candidate.Diagnostic) {
				return fmt.Errorf("merge queue %q contains unsafe diagnostic", repository)
			}
		}
	}
	for _, blocker := range r.Blockers {
		if (blocker.Stage != "" && !safeID(blocker.Stage)) || !safeID(blocker.Code) || !safeDiagnostic(blocker.Message) {
			return errors.New("run contains unsafe blocker evidence")
		}
	}
	for stageID, stage := range r.Stages {
		if stage.DagrTerminalPending == "" {
			continue
		}
		if stage.Reservation == nil || stage.Reservation.Phase != ReservationReleased || !safeID(stage.FleetID) {
			return fmt.Errorf("stage %q pending dagr terminal state lacks released child evidence", stageID)
		}
		if stage.DagrTerminalPending == "done" {
			candidate := candidateForStage(r, stageID)
			if stage.Status != StageMergeReady || stage.FailureSource != "" || candidate == nil || candidate.Status != CandidateMergeReady ||
				stage.Result == "" || stage.Result != candidate.ResultDigest {
				return fmt.Errorf("stage %q pending dagr completion lacks merge-ready evidence", stageID)
			}
		} else {
			if stage.Status != StageFailed || (stage.FailureSource != "child" && stage.FailureSource != "integration") {
				return fmt.Errorf("stage %q pending dagr failure has incompatible provenance", stageID)
			}
			if stage.FailureSource == "integration" {
				candidate := candidateForStage(r, stageID)
				if candidate == nil || candidate.Status != CandidateFailed || stage.Result == "" || stage.Result != candidate.ResultDigest {
					return fmt.Errorf("stage %q pending integration failure lacks terminal evidence", stageID)
				}
			} else if candidateForStage(r, stageID) != nil || stage.Result != "" {
				return fmt.Errorf("stage %q pending child failure has conflicting candidate evidence", stageID)
			}
		}
		if stage.DagrTerminalPending == "" && stage.FailureSource != "" {
			return fmt.Errorf("stage %q has stale terminal failure provenance", stageID)
		}
	}
	return nil
}

func candidateForStage(run *Run, stage string) *MergeCandidate {
	for _, queue := range run.MergeQueue {
		for _, candidate := range queue {
			if candidate != nil && candidate.Stage == stage {
				return candidate
			}
		}
	}
	return nil
}

func safeID(value string) bool {
	return opaqueid.Valid(value)
}

func validStageStatus(status StageStatus) bool {
	switch status {
	case StagePending, StageReady, StageQueued, StageReserved, StageDispatched,
		StageInProgress, StageWaiting, StageNeedsInput, StageBlocked, StageCandidate,
		StageIntegrating, StageMergeReady, StageDone, StageFailed, StageOutOfClaim,
		StageReconcileRequired:
		return true
	default:
		return false
	}
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestManifest(value manifest.Manifest) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func safeDiagnostic(value string) bool {
	if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n\x1b") {
		return false
	}
	return true
}
