package missioncontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

type SourceQuery struct {
	SourceID         string
	Kind             string
	Schema           string
	Locator          string
	ExpectedRevision string
	Effect           string
	CallerRole       string
	Stage            string
}

type SourceQuerier interface {
	Query(context.Context, SourceQuery) (SourceObservation, error)
}

type SourceRegistry struct {
	ByKind                map[string]SourceQuerier
	SergeantMissionSource SourceQuerier
	Now                   func() time.Time
}

type SurveyInput struct {
	Manifest     *manifest.Manifest
	ManifestFile string
	CallerRole   string
	Stage        string
}

func Survey(ctx context.Context, input SurveyInput, registry SourceRegistry) (SourceBundle, error) {
	if input.Manifest == nil || input.ManifestFile == "" {
		return SourceBundle{}, errors.New("source survey input is incomplete")
	}
	if input.Manifest.Spec.MissionFormat != manifest.MissionDeclarationV1Alpha1 {
		return SourceBundle{}, errors.New("source survey is unsupported for reference missions")
	}
	declarationPath := declarationFile(input.Manifest, input.ManifestFile)
	raw, err := readStable(declarationPath)
	if err != nil {
		return SourceBundle{}, compileFailure(classifyReadError(err))
	}
	d, err := decode(raw)
	if err != nil {
		return SourceBundle{}, compileFailure(classifyDecodeError(err))
	}
	if err := validate(d, input.Manifest); err != nil {
		return SourceBundle{}, compileFailure(classifyValidationError(err))
	}
	return surveyValidated(ctx, d, raw, input.CallerRole, input.Stage, registry)
}

func surveyValidated(ctx context.Context, d *declaration, declarationBytes []byte, callerRole, stage string, registry SourceRegistry) (SourceBundle, error) {
	if !callerRoles[callerRole] || (callerRole == "stage") != (stage != "") {
		return SourceBundle{}, errors.New("source survey caller is invalid")
	}
	if err := gateSurvey(d, callerRole, stage); err != nil {
		return SourceBundle{}, err
	}
	now := time.Now().UTC()
	if registry.Now != nil {
		now = registry.Now().UTC()
	}
	observations := make([]SourceObservation, 0, len(d.Spec.Sources))
	for _, declared := range d.Spec.Sources {
		effect := sourceQueryEffect(declared.Kind)
		query := SourceQuery{
			SourceID: declared.ID, Kind: declared.Kind, Schema: declared.Schema, Locator: declared.Locator,
			ExpectedRevision: declared.Revision, Effect: effect, CallerRole: callerRole, Stage: stage,
		}
		querier := registry.ByKind[declared.Kind]
		if declared.Kind == "td" || declared.Kind == "sergeant" {
			querier = registry.SergeantMissionSource
		}
		if querier == nil {
			observations = append(observations, unsupportedObservation(declared, now))
			continue
		}
		observation, err := querier.Query(ctx, query)
		if err != nil {
			return SourceBundle{}, fmt.Errorf("source survey: source=%s reason=query-failed", declared.ID)
		}
		if observation.SourceID != declared.ID || observation.Kind != declared.Kind || observation.Schema != declared.Schema ||
			(declared.Revision != "" && observation.Revision != declared.Revision) {
			return SourceBundle{}, fmt.Errorf("source survey: source=%s reason=mismatched-observation", declared.ID)
		}
		observation.FreshnessPolicy = declared.ObservationPolicy
		if err := observationFresh(observation, now); err != nil {
			return SourceBundle{}, fmt.Errorf("source survey: source=%s reason=stale-or-future", declared.ID)
		}
		observations = append(observations, observation)
	}
	catalogDigest, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		return SourceBundle{}, errors.New("source survey catalog cannot be canonicalized")
	}
	return NewSourceBundle(sha256Hex(declarationBytes), catalogDigest, callerRole, surveyQueryScope(callerRole, stage), observations)
}

func gateSurvey(d *declaration, callerRole, stage string) error {
	if len(d.Spec.Contradictions) != 0 {
		return errors.New("source survey gate refused an unresolved contradiction")
	}
	for _, unknown := range d.Spec.Unknowns {
		if unknown.Blocking {
			return errors.New("source survey gate refused a blocking unknown")
		}
	}
	for _, stop := range d.Spec.Stops {
		if stop.Scope.Entry {
			return errors.New("source survey gate refused an active entry stop")
		}
	}
	allowed := stringSet(d.Spec.Effects.Allowed)
	prohibited := stringSet(d.Spec.Effects.Prohibited)
	needed := map[string]bool{}
	for _, declared := range d.Spec.Sources {
		needed[sourceQueryEffect(declared.Kind)] = true
	}
	for effect := range needed {
		if !allowed[effect] || prohibited[effect] {
			return errors.New("source survey gate refused an undeclared read or query effect")
		}
		if !contains(d.Spec.Effects.Callers[effect], callerRole) {
			return errors.New("source survey gate refused the caller")
		}
		if callerRole == "stage" && !contains(d.Spec.Effects.Stages[stage], effect) {
			return errors.New("source survey gate refused the stage scope")
		}
	}
	return nil
}

func sourceQueryEffect(kind string) string {
	if kind == "receiving-system" || kind == "environment-classifier" {
		return "query-authority"
	}
	return "read-source"
}

func unsupportedObservation(declared source, now time.Time) SourceObservation {
	revision := declared.Revision
	if revision == "" {
		revision = "unsupported"
	}
	return SourceObservation{
		SourceID: declared.ID, Kind: declared.Kind, Schema: declared.Schema, AdapterVersion: "unsupported-v1",
		Revision: revision, Quality: QualityUnsupported, ObservedAt: now.Format(time.RFC3339Nano),
		FreshnessPolicy: declared.ObservationPolicy, Payload: map[string]any{"status": "unsupported"},
	}
}

func declarationFile(m *manifest.Manifest, manifestFile string) string {
	return filepath.Join(filepath.Dir(manifestFile), filepath.FromSlash(m.Spec.Mission))
}

func surveyQueryScope(callerRole, stage string) string {
	if stage == "" {
		return "entry-" + callerRole
	}
	return "stage-" + stage + "-" + callerRole
}

func sha256Hex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
