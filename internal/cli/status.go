package cli

import (
	"sort"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/planner"
	"github.com/mrtnebrle/platoon/internal/state"
)

type StatusReport struct {
	RunID         string             `json:"runId"`
	Status        state.RunStatus    `json:"status"`
	Generation    uint64             `json:"generation"`
	UpdatedAt     time.Time          `json:"updatedAt"`
	Tokens        TokenReport        `json:"tokens"`
	Repositories  []RepositoryReport `json:"repositories"`
	Stages        []StageReport      `json:"stages"`
	Queued        []string           `json:"queued"`
	Running       []string           `json:"running"`
	CriticalReady []string           `json:"criticalReady"`
	Blockers      []state.Blocker    `json:"blockers"`
}

type TokenReport struct {
	ImplementationUsed  int `json:"implementationUsed"`
	ImplementationLimit int `json:"implementationLimit"`
	ReviewUsed          int `json:"reviewUsed"`
	ReviewLimit         int `json:"reviewLimit"`
}

type RepositoryReport struct {
	ID         string                 `json:"id"`
	Claims     []ClaimReport          `json:"claims"`
	MergeQueue []state.MergeCandidate `json:"mergeQueue"`
}

type ClaimReport struct {
	Stage    string   `json:"stage"`
	Paths    []string `json:"paths"`
	Semantic []string `json:"semantic"`
}

type StageReport struct {
	ID         string            `json:"id"`
	Repository string            `json:"repository"`
	Mode       manifest.Mode     `json:"mode"`
	Model      string            `json:"model"`
	Risk       string            `json:"risk"`
	Status     state.StageStatus `json:"status"`
	FleetID    string            `json:"fleetId,omitempty"`
	Adopted    bool              `json:"adopted"`
	Blocker    string            `json:"blocker,omitempty"`
}

func BuildStatus(run *state.Run) StatusReport {
	report := StatusReport{
		RunID: run.ID, Status: run.Status, Generation: run.Generation, UpdatedAt: run.UpdatedAt,
		Tokens: TokenReport{
			ImplementationLimit: run.Manifest.Spec.Limits.Implementation,
			ReviewLimit:         run.Manifest.Spec.Limits.Review,
		},
		Blockers: append([]state.Blocker(nil), run.Blockers...),
	}
	report.Repositories = make([]RepositoryReport, 0, len(run.Manifest.Spec.Repositories))
	repositoryReports := make(map[string]*RepositoryReport, len(run.Manifest.Spec.Repositories))
	for _, repository := range run.Manifest.Spec.Repositories {
		report.Repositories = append(report.Repositories, RepositoryReport{ID: repository.ID, Claims: []ClaimReport{}, MergeQueue: []state.MergeCandidate{}})
		repositoryReports[repository.ID] = &report.Repositories[len(report.Repositories)-1]
	}
	for _, configured := range run.Manifest.Spec.Stages {
		stage := run.Stages[configured.ID]
		if stage == nil {
			continue
		}
		report.Stages = append(report.Stages, StageReport{
			ID: configured.ID, Repository: configured.Repository, Mode: configured.Mode,
			Model: configured.Model, Risk: configured.Risk, Status: stage.Status,
			FleetID: stage.FleetID, Adopted: stage.Adopted, Blocker: stage.Blocker,
		})
		if stage.Reservation != nil && stage.Reservation.Phase != state.ReservationReleased && stage.Reservation.Phase != state.ReservationAbsent {
			if configured.Mode == manifest.Review {
				report.Tokens.ReviewUsed++
			} else {
				report.Tokens.ImplementationUsed++
			}
		}
		if stage.Reservation != nil && stage.Reservation.Phase != state.ReservationAbsent && stage.Status != state.StageDone && stage.Status != state.StageFailed && configured.Mode == manifest.Implementation {
			repositoryReports[configured.Repository].Claims = append(repositoryReports[configured.Repository].Claims, ClaimReport{
				Stage: configured.ID, Paths: append([]string(nil), configured.Claims.Paths...), Semantic: append([]string(nil), configured.Claims.Semantic...),
			})
		}
		switch stage.Status {
		case state.StageReady, state.StageQueued:
			report.Queued = append(report.Queued, configured.ID)
		case state.StageReserved, state.StageDispatched, state.StageInProgress, state.StageWaiting,
			state.StageNeedsInput, state.StageBlocked, state.StageCandidate, state.StageIntegrating, state.StageMergeReady:
			report.Running = append(report.Running, configured.ID)
		}
	}
	for repository, candidates := range run.MergeQueue {
		reportForRepository := repositoryReports[repository]
		if reportForRepository == nil {
			continue
		}
		for _, candidate := range candidates {
			if candidate != nil {
				reportForRepository.MergeQueue = append(reportForRepository.MergeQueue, *candidate)
			}
		}
	}
	for _, priority := range planner.Priorities(&run.Manifest) {
		stage := run.Stages[priority.Stage]
		if stage != nil && (stage.Status == state.StageReady || stage.Status == state.StageQueued) {
			report.CriticalReady = append(report.CriticalReady, priority.Stage)
		}
	}
	sort.Slice(report.Repositories, func(i, j int) bool { return report.Repositories[i].ID < report.Repositories[j].ID })
	sort.Slice(report.Stages, func(i, j int) bool { return report.Stages[i].ID < report.Stages[j].ID })
	sort.Strings(report.Queued)
	sort.Strings(report.Running)
	return report
}
