package missioncontrol

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRevalidateAllowsLaterEquivalentObservationAndRejectsContentChange(t *testing.T) {
	d := surveyDeclaration()
	compiled, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry-operator", []SourceObservation{{
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:00:00Z", Payload: map[string]any{"policyDigest": "stable"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(compiled)
	querier := &recordingSourceQuerier{observations: map[string]SourceObservation{"policy": {
		SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1", Revision: "v1",
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:01:00Z", Payload: map[string]any{"policyDigest": "stable"},
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
		Quality: QualityVerified, ObservedAt: "2026-08-08T10:01:00Z", Payload: map[string]any{"policyDigest": "changed"},
	}
	result, err = revalidateValidated(context.Background(), d, declarationBytes, raw, "operator", "", SourceRegistry{
		ByKind: map[string]SourceQuerier{"platoon-policy": querier},
		Now:    func() time.Time { return time.Date(2026, 8, 8, 10, 1, 1, 0, time.UTC) },
	})
	if err != nil || result.Status != RevalidationReplanRequired {
		t.Fatalf("changed revalidation result=%#v err=%v", result, err)
	}
}

func rebuildBundleMetadata(t *testing.T, bundle SourceBundle, d *declaration, raw []byte) SourceBundle {
	t.Helper()
	catalog, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := NewSourceBundle(sha256Hex(raw), catalog, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}
