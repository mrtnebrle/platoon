package missioncontrol

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

type RevalidationStatus string

const (
	RevalidationValidated      RevalidationStatus = "validated"
	RevalidationReplanRequired RevalidationStatus = "replan_required"
)

type RevalidationResult struct {
	Status           RevalidationStatus `json:"status"`
	Reason           string             `json:"reason,omitempty"`
	BundleID         string             `json:"bundleId,omitempty"`
	ContentSetDigest string             `json:"contentSetDigest,omitempty"`
}

type RevalidateInput struct {
	Manifest     *manifest.Manifest
	ManifestFile string
	BundleBytes  []byte
	CallerRole   string
	Stage        string
}

func Revalidate(ctx context.Context, input RevalidateInput, registry SourceRegistry) (RevalidationResult, error) {
	if input.Manifest == nil || input.ManifestFile == "" || len(input.BundleBytes) == 0 {
		return RevalidationResult{}, errors.New("source revalidation input is incomplete")
	}
	if input.Manifest.Spec.MissionFormat != manifest.MissionDeclarationV1Alpha1 {
		return RevalidationResult{}, errors.New("source revalidation is unsupported for reference missions")
	}
	manifestBytes, err := readStable(input.ManifestFile)
	if err != nil {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "manifest changed or unavailable"}, nil
	}
	currentManifest, err := manifest.Load(manifestBytes)
	if err != nil || !sameManifest(currentManifest, input.Manifest) {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "manifest changed or invalid"}, nil
	}
	declarationBytes, err := readStable(declarationFile(input.Manifest, input.ManifestFile))
	if err != nil {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "declaration changed or unavailable"}, nil
	}
	d, err := decode(declarationBytes)
	if err != nil || validate(d, input.Manifest) != nil {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "declaration changed or invalid"}, nil
	}
	result, err := revalidateValidated(ctx, d, declarationBytes, input.BundleBytes, input.CallerRole, input.Stage, registry)
	if err != nil || result.Status != RevalidationValidated {
		return result, err
	}
	manifestAfter, manifestErr := readStable(input.ManifestFile)
	declarationAfter, declarationErr := readStable(declarationFile(input.Manifest, input.ManifestFile))
	if manifestErr != nil || declarationErr != nil || !bytes.Equal(manifestBytes, manifestAfter) || !bytes.Equal(declarationBytes, declarationAfter) {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "manifest or declaration changed during requery"}, nil
	}
	return result, nil
}

func revalidateValidated(ctx context.Context, d *declaration, declarationBytes, bundleBytes []byte, callerRole, stage string, registry SourceRegistry) (RevalidationResult, error) {
	now := time.Now().UTC()
	if registry.Now != nil {
		now = registry.Now().UTC()
	}
	compiled, err := decodeSourceBundleIdentity(bundleBytes)
	if err != nil {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: bundleReason(err)}, nil
	}
	catalogDigest, err := sourceCatalogDigest(d.Spec.Sources)
	declarationDigest, identityErr := declarationIdentity(declarationBytes)
	if err != nil || identityErr != nil || compiled.DeclarationDigest != declarationDigest || compiled.SourceCatalogDigest != catalogDigest {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "declaration or source catalog changed"}, nil
	}
	if compiled.CallerRole != callerRole || compiled.QueryScope != surveyQueryScope(callerRole, stage) {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "source bundle caller provenance changed"}, nil
	}
	current, err := surveyValidated(ctx, d, declarationBytes, callerRole, stage, registry)
	if err != nil {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "source requery was refused or unavailable"}, nil
	}
	compiledByID := make(map[string]SourceObservation, len(compiled.Observations))
	for _, observation := range compiled.Observations {
		compiledByID[observation.SourceID] = observation
	}
	for _, observation := range current.Observations {
		prior, ok := compiledByID[observation.SourceID]
		if !ok || observation.Schema != prior.Schema || observation.Revision != prior.Revision || observation.Quality != prior.Quality ||
			observation.FreshnessPolicy != prior.FreshnessPolicy ||
			observation.ContentDigest != prior.ContentDigest || observation.Quality != QualityVerified || observationFresh(observation, now) != nil {
			return RevalidationResult{Status: RevalidationReplanRequired, Reason: "source content, revision, quality, or freshness changed"}, nil
		}
	}
	if len(current.Observations) != len(compiled.Observations) {
		return RevalidationResult{Status: RevalidationReplanRequired, Reason: "declared source set changed"}, nil
	}
	return RevalidationResult{
		Status: RevalidationValidated, BundleID: compiled.BundleID, ContentSetDigest: current.ContentSetDigest,
	}, nil
}
