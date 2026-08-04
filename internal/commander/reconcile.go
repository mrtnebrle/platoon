package commander

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mrtnebrle/platoon/internal/adapter"
	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/state"
)

func (c *Commander) Reconcile(ctx context.Context, runID string) (run *state.Run, returnErr error) {
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
		return run, nil
	}
	if err := c.verifyProvenance(run); err != nil {
		return run, err
	}
	run.Generation = lease.Generation()
	switch run.Dagr.Phase {
	case "starting", "loading_workflow", "workflow_loaded":
		workflow, workflowErr := adapter.WorkflowYAML(run.Dagr.Workflow, run.Manifest.Spec.Stages)
		if workflowErr != nil {
			return run, workflowErr
		}
		workflowPath, writeErr := c.store.WriteRunFile(run.ID, "workflow.yaml", workflow, lease)
		if writeErr != nil {
			return run, writeErr
		}
		if run.Dagr.Phase == "starting" {
			run.Dagr.Phase = "loading_workflow"
			if err := c.save(run, lease); err != nil {
				return run, err
			}
		}
		if err := c.prepareDagr(ctx, run, lease, workflowPath, sortedStageNames(&run.Manifest)); err != nil {
			return run, err
		}
		if err := c.startPreparedDagrRun(ctx, run, lease); err != nil {
			return run, err
		}
	case "prepared":
		if err := c.startPreparedDagrRun(ctx, run, lease); err != nil {
			return run, err
		}
	case "starting_run":
		recovery, recoveryErr := c.dagr.RecoverRun(ctx, run.Dagr.Workflow)
		if recoveryErr != nil {
			return run, recoveryErr
		}
		switch recovery.State {
		case adapter.DagrRunAbsent:
			run.Dagr.Phase = "prepared"
			if err := c.save(run, lease); err != nil {
				return run, err
			}
			if err := c.startPreparedDagrRun(ctx, run, lease); err != nil {
				return run, err
			}
		case adapter.DagrRunFound:
			run.Dagr.RunID = recovery.RunID
			run.Dagr.Phase = "active"
			run.Status = state.RunActive
			if _, err := c.dagr.Snapshot(ctx, recovery.RunID, sortedStageNames(&run.Manifest)); err != nil {
				return run, err
			}
			clearBlocker(run, "", "dagr_start_uncertain")
			clearBlocker(run, "", "dagr_start_ambiguous")
			if err := c.save(run, lease); err != nil {
				return run, err
			}
		case adapter.DagrRunAmbiguous:
			addBlocker(run, "", "dagr_start_ambiguous", "dagr has a run whose full identity is unavailable")
			cause := errors.New("multiple dagr runs exist for the unique workflow")
			return run, errors.Join(cause, c.save(run, lease))
		default:
			return run, errors.New("dagr recovery returned an unsupported state")
		}
	case "active":
	default:
		addBlocker(run, "", "dagr_reconcile_required", "dagr identity is not sufficient for automatic recovery")
		cause := errors.New("dagr run requires manual identity reconciliation")
		return run, errors.Join(cause, c.save(run, lease))
	}
	if run.Status == state.RunReconcileRequired {
		run.Status = state.RunActive
	} else if run.Status == state.RunDrained && run.PreDrainStatus == state.RunReconcileRequired {
		run.PreDrainStatus = state.RunActive
	}
	for _, configured := range prioritizedStages(&run.Manifest) {
		stage := run.Stages[configured.ID]
		if stage.DagrTerminalPending == "" {
			continue
		}
		success := stage.DagrTerminalPending == "done"
		if success {
			candidate := candidateForStage(run, configured.Repository, configured.ID)
			evidence, evidenceErr := c.fleets.Read(stage.FleetID, configured.Repository, c.bindingForStage(run, configured, stage))
			if evidenceErr != nil || candidate == nil || !candidateEvidenceMatches(candidate, evidence) {
				invalidateCandidate(run, configured.Repository, configured.ID, "pending completion evidence changed")
				stage.Status = state.StageBlocked
				stage.Blocker = "pending completion evidence changed"
				addBlocker(run, configured.ID, "candidate_evidence_changed", stage.Blocker)
				if saveErr := c.save(run, lease); saveErr != nil {
					return run, errors.Join(evidenceErr, saveErr)
				}
				return run, evidenceErr
			}
			changed, diffErr := c.diff.ChangedPaths(ctx, evidence.Worktree, evidence.InitialSHA)
			if diffErr != nil {
				return run, diffErr
			}
			if violations := adapter.CheckPathClaims(changed, configured.Claims.Paths); len(violations) != 0 {
				message := sanitizeViolationMessage(violations)
				invalidateCandidate(run, configured.Repository, configured.ID, message)
				stage.Status = state.StageOutOfClaim
				stage.Blocker = message
				addBlocker(run, configured.ID, "out_of_claim", message)
				return run, c.save(run, lease)
			}
			dagrSnapshot, snapshotErr := c.dagr.Snapshot(ctx, run.Dagr.RunID, sortedStageNames(&run.Manifest))
			if snapshotErr != nil {
				return run, snapshotErr
			}
			if dagrSnapshot[configured.ID] != adapter.DagrDone {
				repository, _ := run.Manifest.Repository(configured.Repository)
				head, headErr := c.integrator.Head(ctx, repository)
				if headErr != nil {
					return run, headErr
				}
				containsBase, ancestryErr := c.integrator.ContainsBase(ctx, evidence.Worktree, head)
				if ancestryErr != nil {
					return run, ancestryErr
				}
				if head != candidate.BaseSHA || !containsBase {
					candidate.BaseSHA = head
					candidate.Status = state.CandidateQueued
					candidate.Diagnostic = "repository base changed before pending dagr completion"
					stage.DagrTerminalPending = ""
					stage.Status = state.StageCandidate
					return run, c.save(run, lease)
				}
			}
		} else {
			switch stage.FailureSource {
			case "child":
				evidence, evidenceErr := c.fleets.Read(stage.FleetID, configured.Repository, c.bindingForStage(run, configured, stage))
				if evidenceErr != nil || evidence.Status != adapter.FleetFailed {
					stage.DagrTerminalPending = ""
					stage.Status = state.StageBlocked
					stage.Blocker = "pending child failure evidence changed"
					addBlocker(run, configured.ID, "candidate_evidence_changed", stage.Blocker)
					return run, errors.Join(evidenceErr, c.save(run, lease))
				}
			case "integration":
				candidate := candidateForStage(run, configured.Repository, configured.ID)
				evidence, evidenceErr := c.fleets.Read(stage.FleetID, configured.Repository, c.bindingForStage(run, configured, stage))
				if evidenceErr != nil || candidate == nil || !candidateEvidenceMatches(candidate, evidence) {
					stage.DagrTerminalPending = ""
					stage.Status = state.StageBlocked
					stage.Blocker = "pending integration failure evidence changed"
					addBlocker(run, configured.ID, "candidate_evidence_changed", stage.Blocker)
					return run, errors.Join(evidenceErr, c.save(run, lease))
				}
				changed, diffErr := c.diff.ChangedPaths(ctx, evidence.Worktree, evidence.InitialSHA)
				if diffErr != nil {
					return run, diffErr
				}
				if violations := adapter.CheckPathClaims(changed, configured.Claims.Paths); len(violations) != 0 {
					message := sanitizeViolationMessage(violations)
					stage.DagrTerminalPending = ""
					stage.Status = state.StageOutOfClaim
					stage.Blocker = message
					addBlocker(run, configured.ID, "out_of_claim", message)
					return run, c.save(run, lease)
				}
			default:
				return run, errors.New("pending failure source is invalid")
			}
		}
		if err := c.advanceDagr(ctx, run, configured.ID, success); err != nil {
			markReconcileRequired(run)
			addBlocker(run, configured.ID, "dagr_terminal_uncertain", "dagr terminal transition remains unverified")
			return run, errors.Join(err, c.save(run, lease))
		}
		stage.DagrTerminalPending = ""
		stage.FailureSource = ""
		if success {
			stage.Status = state.StageDone
		} else {
			stage.Status = state.StageFailed
		}
		clearBlocker(run, configured.ID, "dagr_terminal_uncertain")
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}

	unresolved := false
	for _, configured := range prioritizedStages(&run.Manifest) {
		stage := run.Stages[configured.ID]
		if stage.Reservation != nil && stage.Reservation.Phase == state.ReservationPrepared {
			if err := c.dispatchPreparedStage(ctx, run, lease, configured, stage); err != nil {
				return run, err
			}
		}
		if stage.Reservation != nil && (stage.Reservation.Phase == state.ReservationDispatching || stage.Reservation.Phase == state.ReservationReconcileRequired) {
			fleetID := stage.Reservation.FleetID
			if fleetID == "" {
				matches, scanErr := c.fleets.FindByCorrelation(run.Manifest.Spec.Adapters.Sergeant.OriginProfile, stage.Reservation.CorrelationID)
				if scanErr != nil {
					stage.Reservation.Phase = state.ReservationReconcileRequired
					stage.Status = state.StageReconcileRequired
					stage.Blocker = "dispatch correlation evidence could not be inspected"
					addBlocker(run, configured.ID, "dispatch_ambiguous", stage.Blocker)
					unresolved = true
					continue
				}
				switch len(matches) {
				case 0:
					if stage.Reservation.DispatchAttempts < 2 {
						stage.Reservation.Phase = state.ReservationPrepared
						stage.Status = state.StageReserved
						stage.Blocker = ""
						if err := c.save(run, lease); err != nil {
							return run, err
						}
						if err := c.dispatchPreparedStage(ctx, run, lease, configured, stage); err != nil {
							return run, err
						}
						continue
					}
					stage.Reservation.Phase = state.ReservationAbsent
					stage.Status = state.StageBlocked
					stage.Blocker = "no child exists after bounded dispatch recovery; reservation released"
					addBlocker(run, configured.ID, "dispatch_ambiguous", stage.Blocker)
					unresolved = true
					continue
				case 1:
					fleetID = matches[0]
				default:
					stage.Reservation.Phase = state.ReservationReconcileRequired
					stage.Status = state.StageReconcileRequired
					stage.Blocker = "multiple fleets match one dispatch correlation"
					addBlocker(run, configured.ID, "dispatch_ambiguous", stage.Blocker)
					unresolved = true
					continue
				}
			}
			evidence, evidenceErr := c.fleets.Read(fleetID, configured.Repository, c.correlatedBinding(run, configured, stage.Reservation.CorrelationID))
			if evidenceErr != nil {
				stage.Reservation.Phase = state.ReservationReconcileRequired
				stage.Status = state.StageReconcileRequired
				stage.Blocker = "correlated fleet evidence did not verify"
				addBlocker(run, configured.ID, "dispatch_unverified", stage.Blocker)
				unresolved = true
				continue
			}
			stage.FleetID = evidence.FleetID
			stage.Reservation.FleetID = evidence.FleetID
			stage.Reservation.Phase = state.ReservationCommitted
			stage.Status = stageStatusFromFleet(evidence.Status)
			stage.Blocker = ""
			clearBlocker(run, configured.ID, "dispatch_ambiguous")
			clearBlocker(run, configured.ID, "dispatch_unverified")
			clearBlocker(run, configured.ID, "dispatch_uncertain")
		}
		if configured.AdoptFleet != "" && stage.FleetID == "" {
			evidence, evidenceErr := c.fleets.Read(configured.AdoptFleet, configured.Repository, c.binding(run, configured))
			if evidenceErr != nil {
				stage.Status = state.StageBlocked
				stage.Blocker = "adoption evidence remains unverified"
				addBlocker(run, configured.ID, "adoption_unverified", stage.Blocker)
				unresolved = true
				continue
			}
			c.commitAdoption(run, configured, evidence, lease.Generation())
			clearBlocker(run, configured.ID, "adoption_unverified")
		}
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}
	if c.adoptedPolicyConflict(run) {
		unresolved = true
	}

	for _, configured := range prioritizedStages(&run.Manifest) {
		stage := run.Stages[configured.ID]
		if stage.FleetID == "" || stage.Status == state.StageDone || stage.Status == state.StageFailed || stage.Status == state.StageOutOfClaim {
			continue
		}
		evidence, evidenceErr := c.fleets.Read(stage.FleetID, configured.Repository, c.bindingForStage(run, configured, stage))
		if evidenceErr != nil {
			stage.Status = state.StageBlocked
			stage.Blocker = "child fleet evidence became unavailable or mismatched"
			invalidateCandidate(run, configured.Repository, configured.ID, stage.Blocker)
			addBlocker(run, configured.ID, "fleet_unverified", stage.Blocker)
			unresolved = true
			continue
		}
		clearBlocker(run, configured.ID, "fleet_unverified")
		switch evidence.Status {
		case adapter.FleetDone:
			releaseReservation(stage, c.now().UTC())
			changed, diffErr := c.diff.ChangedPaths(ctx, evidence.Worktree, evidence.InitialSHA)
			if diffErr != nil {
				stage.Status = state.StageBlocked
				stage.Blocker = "changed paths could not be verified"
				invalidateCandidate(run, configured.Repository, configured.ID, stage.Blocker)
				addBlocker(run, configured.ID, "diff_unverified", stage.Blocker)
				unresolved = true
				continue
			}
			clearBlocker(run, configured.ID, "diff_unverified")
			violations := adapter.CheckPathClaims(changed, configured.Claims.Paths)
			if len(violations) != 0 {
				stage.Status = state.StageOutOfClaim
				stage.Blocker = sanitizeViolationMessage(violations)
				invalidateCandidate(run, configured.Repository, configured.ID, stage.Blocker)
				addBlocker(run, configured.ID, "out_of_claim", stage.Blocker)
				unresolved = true
				continue
			}
			stage.Result = evidence.ResultDigest
			enqueueCandidate(run, configured.Repository, configured.ID, evidence, lease.Generation())
			stage.Status = state.StageCandidate
			if candidate := candidateForStage(run, configured.Repository, configured.ID); candidate != nil {
				switch candidate.Status {
				case state.CandidateIntegrating:
					stage.Status = state.StageIntegrating
				case state.CandidateMergeReady:
					stage.Status = state.StageMergeReady
				case state.CandidateBlocked:
					stage.Status = state.StageBlocked
				case state.CandidateFailed:
					stage.Status = state.StageFailed
				}
			}
		case adapter.FleetFailed:
			releaseReservation(stage, c.now().UTC())
			removeCandidate(run, configured.Repository, configured.ID)
			stage.Status = state.StageFailed
			stage.DagrTerminalPending = "failed"
			stage.FailureSource = "child"
			stage.Result = ""
			stage.Blocker = "child fleet reported verified terminal failure"
			addBlocker(run, configured.ID, "child_failed", stage.Blocker)
			if err := c.save(run, lease); err != nil {
				return run, err
			}
			if err := c.advanceDagr(ctx, run, configured.ID, false); err != nil {
				markReconcileRequired(run)
				addBlocker(run, configured.ID, "dagr_terminal_uncertain", "dagr failure transition could not be verified")
				return run, errors.Join(err, c.save(run, lease))
			}
			stage.DagrTerminalPending = ""
			stage.FailureSource = ""
			clearBlocker(run, configured.ID, "dagr_terminal_uncertain")
		default:
			if candidateForStage(run, configured.Repository, configured.ID) != nil {
				stage.Status = state.StageBlocked
				stage.Blocker = "candidate child is no longer terminal done"
				invalidateCandidate(run, configured.Repository, configured.ID, stage.Blocker)
				addBlocker(run, configured.ID, "candidate_evidence_changed", stage.Blocker)
				unresolved = true
			} else {
				stage.Status = stageStatusFromFleet(evidence.Status)
				stage.Blocker = ""
			}
		}
	}
	if err := c.save(run, lease); err != nil {
		return run, err
	}

	if err := c.processMergeQueues(ctx, run, lease); err != nil {
		return run, err
	}
	snapshot, err := c.dagr.Snapshot(ctx, run.Dagr.RunID, sortedStageNames(&run.Manifest))
	if err != nil {
		markReconcileRequired(run)
		addBlocker(run, "", "dagr_snapshot_unverified", "dagr readiness could not be verified")
		return run, errors.Join(err, c.save(run, lease))
	}
	clearBlocker(run, "", "dagr_snapshot_unverified")
	c.applySnapshot(run, snapshot)
	markUnreachableStages(run)
	if hasSafetyBlock(run) {
		unresolved = true
	}
	if unresolved {
		markReconcileRequired(run)
	}
	if !unresolved && run.Status != state.RunDrained {
		run.Status = state.RunActive
		if err := c.admitReady(ctx, run, lease, snapshot); err != nil {
			return run, err
		}
	}
	updateTerminalStatus(run)
	if err := c.save(run, lease); err != nil {
		return run, err
	}
	return run, nil
}

func (c *Commander) processMergeQueues(ctx context.Context, run *state.Run, lease *state.Lease) error {
	repositories := append([]manifest.Repository(nil), run.Manifest.Spec.Repositories...)
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].ID < repositories[j].ID })
	for _, repository := range repositories {
		queue := run.MergeQueue[repository.ID]
		recovered := false
		for _, candidate := range queue {
			if candidate.Status == state.CandidateIntegrating {
				candidate.Status = state.CandidateQueued
				candidate.Diagnostic = "prior integration attempt has no durable success evidence"
				run.Stages[candidate.Stage].Status = state.StageCandidate
				recovered = true
			}
		}
		if recovered {
			if err := c.save(run, lease); err != nil {
				return err
			}
			continue
		}
		var candidate *state.MergeCandidate
		for _, queued := range queue {
			if queued.Status == state.CandidateQueued {
				candidate = queued
				break
			}
		}
		if candidate == nil {
			continue
		}
		stageState := run.Stages[candidate.Stage]
		configured, _ := run.Manifest.Stage(candidate.Stage)
		if stageState.Status != state.StageCandidate {
			invalidateCandidate(run, repository.ID, candidate.Stage, "candidate stage is no longer eligible for integration")
			if err := c.save(run, lease); err != nil {
				return err
			}
			continue
		}
		evidence, err := c.fleets.Read(candidate.FleetID, configured.Repository, c.bindingForStage(run, configured, stageState))
		if err != nil || !candidateEvidenceMatches(candidate, evidence) {
			invalidateCandidate(run, repository.ID, candidate.Stage, "candidate terminal evidence changed before integration")
			stageState.Status = state.StageBlocked
			stageState.Blocker = "candidate terminal evidence changed before integration"
			addBlocker(run, candidate.Stage, "candidate_evidence_changed", stageState.Blocker)
			if saveErr := c.save(run, lease); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			if err != nil {
				return err
			}
			continue
		}
		changed, err := c.diff.ChangedPaths(ctx, evidence.Worktree, evidence.InitialSHA)
		if err != nil {
			return err
		}
		if violations := adapter.CheckPathClaims(changed, configured.Claims.Paths); len(violations) != 0 {
			invalidateCandidate(run, repository.ID, candidate.Stage, sanitizeViolationMessage(violations))
			stageState.Status = state.StageOutOfClaim
			stageState.Blocker = sanitizeViolationMessage(violations)
			addBlocker(run, candidate.Stage, "out_of_claim", stageState.Blocker)
			return c.save(run, lease)
		}
		head, err := c.integrator.Head(ctx, repository)
		if err != nil {
			candidate.Diagnostic = "repository base could not be verified"
			addBlocker(run, candidate.Stage, "base_unverified", candidate.Diagnostic)
			return errors.Join(err, c.save(run, lease))
		}
		containsBase, err := c.integrator.ContainsBase(ctx, evidence.Worktree, head)
		if err != nil {
			return err
		}
		candidate.BaseSHA = head
		if !containsBase {
			candidate.Diagnostic = "candidate worktree does not contain the current repository base"
			stageState.Status = state.StageCandidate
			if err := c.save(run, lease); err != nil {
				return err
			}
			continue
		}
		clearBlocker(run, candidate.Stage, "base_unverified")
		candidate.Status = state.CandidateIntegrating
		candidate.Generation = lease.Generation()
		candidate.Attempts++
		candidate.Diagnostic = ""
		stageState.Status = state.StageIntegrating
		if err := c.save(run, lease); err != nil {
			return err
		}
		commands := append([]manifest.Command(nil), repository.Integration...)
		commands = append(commands, configured.Acceptance...)
		integrationErr := c.integrator.Run(ctx, evidence.Worktree, commands)
		postEvidence, err := c.fleets.Read(candidate.FleetID, configured.Repository, c.bindingForStage(run, configured, stageState))
		if err != nil || !candidateEvidenceMatches(candidate, postEvidence) {
			invalidateCandidate(run, repository.ID, candidate.Stage, "candidate terminal evidence changed during integration")
			stageState.Status = state.StageBlocked
			stageState.Blocker = "candidate terminal evidence changed during integration"
			addBlocker(run, candidate.Stage, "candidate_evidence_changed", stageState.Blocker)
			if saveErr := c.save(run, lease); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			if err != nil {
				return err
			}
			continue
		}
		postChanged, err := c.diff.ChangedPaths(ctx, postEvidence.Worktree, postEvidence.InitialSHA)
		if err != nil {
			return err
		}
		if violations := adapter.CheckPathClaims(postChanged, configured.Claims.Paths); len(violations) != 0 {
			message := sanitizeViolationMessage(violations)
			invalidateCandidate(run, repository.ID, candidate.Stage, message)
			stageState.Status = state.StageOutOfClaim
			stageState.Blocker = message
			addBlocker(run, candidate.Stage, "out_of_claim", message)
			return c.save(run, lease)
		}
		if integrationErr != nil {
			candidate.Status = state.CandidateFailed
			candidate.Diagnostic = "configured integration command failed"
			stageState.Status = state.StageFailed
			stageState.DagrTerminalPending = "failed"
			stageState.FailureSource = "integration"
			stageState.Blocker = candidate.Diagnostic
			addBlocker(run, candidate.Stage, "integration_failed", candidate.Diagnostic)
			if saveErr := c.save(run, lease); saveErr != nil {
				return errors.Join(integrationErr, saveErr)
			}
			if dagrErr := c.advanceDagr(ctx, run, candidate.Stage, false); dagrErr != nil {
				markReconcileRequired(run)
				addBlocker(run, candidate.Stage, "dagr_terminal_uncertain", "dagr failure transition could not be verified")
				return errors.Join(dagrErr, c.save(run, lease))
			}
			stageState.DagrTerminalPending = ""
			stageState.FailureSource = ""
			clearBlocker(run, candidate.Stage, "dagr_terminal_uncertain")
			continue
		}
		currentHead, err := c.integrator.Head(ctx, repository)
		if err != nil {
			candidate.Diagnostic = "repository base changed or became unavailable during integration"
			addBlocker(run, candidate.Stage, "base_unverified", candidate.Diagnostic)
			return errors.Join(err, c.save(run, lease))
		}
		if currentHead != candidate.BaseSHA {
			candidate.BaseSHA = currentHead
			candidate.Status = state.CandidateQueued
			candidate.Diagnostic = "repository base changed during integration; candidate requeued"
			stageState.Status = state.StageCandidate
			if err := c.save(run, lease); err != nil {
				return err
			}
			continue
		}
		containsBase, err = c.integrator.ContainsBase(ctx, postEvidence.Worktree, currentHead)
		if err != nil {
			return err
		}
		if !containsBase {
			candidate.Status = state.CandidateQueued
			candidate.Diagnostic = "candidate lost current-base ancestry during integration"
			stageState.Status = state.StageCandidate
			if err := c.save(run, lease); err != nil {
				return err
			}
			continue
		}
		candidate.Status = state.CandidateMergeReady
		stageState.Status = state.StageMergeReady
		stageState.DagrTerminalPending = "done"
		if err := c.save(run, lease); err != nil {
			return err
		}
		if err := c.advanceDagr(ctx, run, candidate.Stage, true); err != nil {
			markReconcileRequired(run)
			addBlocker(run, candidate.Stage, "dagr_terminal_uncertain", "dagr completion transition could not be verified")
			return errors.Join(err, c.save(run, lease))
		}
		stageState.DagrTerminalPending = ""
		clearBlocker(run, candidate.Stage, "dagr_terminal_uncertain")
		stageState.Status = state.StageDone
		stageState.Blocker = ""
		if err := c.save(run, lease); err != nil {
			return err
		}
	}
	return nil
}

func (c *Commander) advanceDagr(ctx context.Context, run *state.Run, stageID string, success bool) error {
	dagrStageID := run.Dagr.Stages[stageID]
	if dagrStageID == "" {
		return errors.New("dagr stage identity is missing")
	}
	want := adapter.DagrFailed
	if success {
		want = adapter.DagrDone
	}
	snapshot, err := c.dagr.Snapshot(ctx, run.Dagr.RunID, sortedStageNames(&run.Manifest))
	if err != nil {
		return err
	}
	if snapshot[stageID] == want {
		return nil
	}
	if snapshot[stageID] == adapter.DagrDone || snapshot[stageID] == adapter.DagrFailed || snapshot[stageID] == adapter.DagrSkipped {
		return fmt.Errorf("dagr stage %q has incompatible terminal state %q", stageID, snapshot[stageID])
	}
	if err := c.dagr.SetTerminal(ctx, run.Dagr.RunID, dagrStageID, success); err != nil {
		return err
	}
	snapshot, err = c.dagr.Snapshot(ctx, run.Dagr.RunID, sortedStageNames(&run.Manifest))
	if err != nil {
		return err
	}
	if snapshot[stageID] != want {
		return fmt.Errorf("dagr terminal transition for %q was not durable", stageID)
	}
	return nil
}

func releaseReservation(stage *state.StageState, now time.Time) {
	if stage.Reservation == nil || stage.Reservation.Phase == state.ReservationReleased {
		return
	}
	stage.Reservation.Phase = state.ReservationReleased
	stage.Reservation.ReleasedAt = now
}

func enqueueCandidate(run *state.Run, repository, stage string, evidence adapter.FleetEvidence, generation uint64) {
	for _, candidate := range run.MergeQueue[repository] {
		if candidate.Stage == stage {
			return
		}
	}
	run.MergeQueue[repository] = append(run.MergeQueue[repository], &state.MergeCandidate{
		Stage: stage, FleetID: evidence.FleetID, Status: state.CandidateQueued,
		BaseSHA: evidence.InitialSHA, Worktree: evidence.Worktree, InitialSHA: evidence.InitialSHA,
		ResultDigest: evidence.ResultDigest, Generation: generation,
	})
}

func invalidateCandidate(run *state.Run, repository, stage, diagnostic string) {
	if candidate := candidateForStage(run, repository, stage); candidate != nil && candidate.Status != state.CandidateFailed {
		candidate.Status = state.CandidateBlocked
		candidate.Diagnostic = diagnostic
		if stageState := run.Stages[stage]; stageState != nil {
			stageState.DagrTerminalPending = ""
			stageState.FailureSource = ""
		}
	}
}

func removeCandidate(run *state.Run, repository, stage string) {
	queue := run.MergeQueue[repository]
	filtered := queue[:0]
	for _, candidate := range queue {
		if candidate == nil || candidate.Stage != stage {
			filtered = append(filtered, candidate)
		}
	}
	run.MergeQueue[repository] = filtered
}

func candidateEvidenceMatches(candidate *state.MergeCandidate, evidence adapter.FleetEvidence) bool {
	return evidence.Status == adapter.FleetDone && evidence.FleetID == candidate.FleetID &&
		evidence.Worktree == candidate.Worktree && evidence.InitialSHA == candidate.InitialSHA &&
		evidence.ResultDigest == candidate.ResultDigest
}

func candidateForStage(run *state.Run, repository, stage string) *state.MergeCandidate {
	for _, candidate := range run.MergeQueue[repository] {
		if candidate.Stage == stage {
			return candidate
		}
	}
	return nil
}

func hasSafetyBlock(run *state.Run) bool {
	for _, stage := range run.Stages {
		switch stage.Status {
		case state.StageReconcileRequired, state.StageOutOfClaim:
			return true
		}
	}
	for _, queue := range run.MergeQueue {
		for _, candidate := range queue {
			if candidate != nil && candidate.Status == state.CandidateBlocked {
				return true
			}
		}
	}
	for _, stage := range run.Stages {
		if stage != nil && stage.Reservation != nil && stage.Reservation.Phase == state.ReservationAbsent {
			return true
		}
	}
	return false
}

func updateTerminalStatus(run *state.Run) {
	allTerminal := true
	failed := false
	for _, stage := range run.Stages {
		switch stage.Status {
		case state.StageDone:
		case state.StageFailed:
			failed = true
		default:
			allTerminal = false
		}
	}
	if !allTerminal {
		return
	}
	if failed {
		run.Status = state.RunFailed
	} else {
		run.Status = state.RunCompleted
	}
	run.PreDrainStatus = ""
}

func markUnreachableStages(run *state.Run) {
	changed := true
	for changed {
		changed = false
		for _, configured := range run.Manifest.Spec.Stages {
			stage := run.Stages[configured.ID]
			if stage == nil || stage.FleetID != "" || stage.Status == state.StageDone || stage.Status == state.StageFailed {
				continue
			}
			for _, dependency := range configured.DependsOn {
				if dependencyState := run.Stages[dependency]; dependencyState != nil && dependencyState.Status == state.StageFailed {
					stage.Status = state.StageFailed
					stage.Blocker = "dependency failed before this stage was dispatched"
					addBlocker(run, configured.ID, "dependency_failed", stage.Blocker)
					changed = true
					break
				}
			}
		}
	}
}
