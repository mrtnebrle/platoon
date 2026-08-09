package missioncontrol

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRevalidateAllowsLaterEquivalentObservationAndRejectsContentChange(t *testing.T) {
	d := surveyDeclaration()
	compiled, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry-operator", []SourceObservation{{
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:00:00Z", Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(compiled)
	querier := &recordingSourceQuerier{observations: map[string]SourceObservation{"policy": {
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:01:00Z", Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)},
	}}}
	declarationBytes := []byte("synthetic declaration")
	// Revalidation binds the exact declaration/catalog supplied at compilation.
	raw, _ = json.Marshal(rebuildBundleMetadata(t, compiled, d, declarationBytes))
	result, err := revalidateValidated(context.Background(), d, declarationBytes, raw, "operator", "", SourceRegistry{
		ByKind: map[string]SourceQuerier{"platoon-policy": querier},
		Now:    func() time.Time { return time.Date(2026, 8, 8, 10, 1, 1, 0, time.UTC) },
	})
	if err != nil || result.Status != RevalidationValidated {
		t.Fatalf("equivalent revalidation result=%#v err=%v", result, err)
	}

	querier.observations["policy"] = SourceObservation{
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:01:00Z", Payload: map[string]any{"policyDigest": strings.Repeat("b", 64)},
	}
	result, err = revalidateValidated(context.Background(), d, declarationBytes, raw, "operator", "", SourceRegistry{
		ByKind: map[string]SourceQuerier{"platoon-policy": querier},
		Now:    func() time.Time { return time.Date(2026, 8, 8, 10, 1, 1, 0, time.UTC) },
	})
	if err != nil || result.Status != RevalidationReplanRequired {
		t.Fatalf("changed revalidation result=%#v err=%v", result, err)
	}
}

func TestRevalidateUsesCurrentFreshnessAndBindsCaller(t *testing.T) {
	d := surveyDeclaration()
	d.Spec.Sources[0].Revision = ""
	d.Spec.Sources[0].ObservationPolicy = "max-age:30s"
	declarationBytes := []byte("synthetic declaration")
	catalog, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		t.Fatal(err)
	}
	declarationDigest, err := declarationIdentity(declarationBytes)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := NewSourceBundle(declarationDigest, catalog, "operator", "entry-operator", []SourceObservation{{
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "observed-v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:00:00Z", FreshnessPolicy: "max-age:30s", Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(compiled)
	querier := &recordingSourceQuerier{observations: map[string]SourceObservation{"policy": {
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "observed-v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:01:00Z", Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)},
	}}}
	registry := SourceRegistry{ByKind: map[string]SourceQuerier{"platoon-policy": querier}, Now: func() time.Time {
		return time.Date(2026, 8, 8, 10, 1, 10, 0, time.UTC)
	}}
	result, err := revalidateValidated(context.Background(), d, declarationBytes, raw, "operator", "", registry)
	if err != nil || result.Status != RevalidationValidated {
		t.Fatalf("fresh current observation result=%#v err=%v", result, err)
	}
	result, err = revalidateValidated(context.Background(), d, declarationBytes, raw, "platoon", "", registry)
	if err != nil || result.Status != RevalidationReplanRequired {
		t.Fatalf("cross-caller result=%#v err=%v", result, err)
	}
}

func rebuildBundleMetadata(t *testing.T, bundle SourceBundle, d *declaration, raw []byte) SourceBundle {
	t.Helper()
	catalog, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		t.Fatal(err)
	}
	declarationDigest, err := declarationIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := NewSourceBundle(declarationDigest, catalog, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}
