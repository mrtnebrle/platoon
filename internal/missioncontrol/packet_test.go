package missioncontrol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestCompileWithBundleProducesDeterministicPacketAndConsumesNoSources(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt := time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC)

	first, err := CompileWithBundle(m, manifestPath, raw, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompileWithBundle(m, manifestPath, raw, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Ready || first.Packet == nil || first.Packet.ID == "" || first.Packet.ID != second.Packet.ID {
		t.Fatalf("packet preview is not deterministic: %#v %#v", first, second)
	}

	changed := bundle
	changed.Observations = append([]SourceObservation(nil), bundle.Observations...)
	changed.Observations[0].Payload = testPayload(changed.Observations[0].Kind, strings.Repeat("b", 64))
	changed, err = NewSourceBundle(changed.DeclarationDigest, changed.SourceCatalogDigest, changed.CallerRole, changed.QueryScope, changed.Observations)
	if err != nil {
		t.Fatal(err)
	}
	changedRaw, _ := json.Marshal(changed)
	changedPreview, err := CompileWithBundle(m, manifestPath, changedRaw, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if changedPreview.Packet == nil || changedPreview.Packet.ID == first.Packet.ID {
		t.Fatal("semantic source change did not change packet identity")
	}
}

func TestPacketIdentityBindsExactManifestAndDeclarationSourceBytes(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	declarationPath := filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(m.Spec.Mission))
	declarationBytes, err := os.ReadFile(declarationPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := decode(declarationBytes)
	if err != nil {
		t.Fatal(err)
	}
	intentBytes, err := os.ReadFile(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(m.Spec.Intent)))
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")

	first, err := compilePacket(m, declarationBytes, d, manifestBytes, intentBytes, bundle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compilePacket(m, append(append([]byte(nil), declarationBytes...), '\n'), d, manifestBytes, intentBytes, bundle)
	if err != nil {
		t.Fatal(err)
	}
	third, err := compilePacket(m, declarationBytes, d, append(append([]byte(nil), manifestBytes...), '\n'), intentBytes, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == third.ID {
		t.Fatalf("exact source-byte change did not change packet identity: %s %s %s", first.ID, second.ID, third.ID)
	}
}

func TestCompileWithBundleReturnsNotReadyForMismatchedOrInconclusiveSource(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	bundle.Observations[0].Quality = QualityInconclusive
	bundle.Observations[0].Payload = map[string]any{"status": "inconclusive"}
	bundle, err = NewSourceBundle(bundle.DeclarationDigest, bundle.SourceCatalogDigest, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bundle)
	preview, err := CompileWithBundle(m, manifestPath, raw, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Ready || preview.Packet != nil || len(preview.Sufficiency) == 0 || !strings.Contains(preview.Sufficiency[0].Reason, "inconclusive") {
		t.Fatalf("inconclusive preview = %#v", preview)
	}
}

func TestStopMatchesObservationQualityOutsidePayload(t *testing.T) {
	observation := SourceObservation{Quality: QualityVerified, Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)}}
	if stopMatches(stopPredicate{Field: "quality", Operator: "quality_is", Value: "unavailable"}, observation) {
		t.Fatal("verified observation activated unavailable-quality stop")
	}
	if !stopMatches(stopPredicate{Field: "quality", Operator: "quality_is", Value: "verified"}, observation) {
		t.Fatal("verified observation did not activate verified-quality stop")
	}
}

func TestPacketNormalizationSortsNestedSchemaSets(t *testing.T) {
	left := map[string]any{"effects": map[string]any{"callers": map[string]any{"read-source": []any{"platoon", "operator"}}}}
	right := map[string]any{"effects": map[string]any{"callers": map[string]any{"read-source": []any{"operator", "platoon"}}}}
	leftJSON, _ := canonicalJSON(normalizePacketValue(left, ""))
	rightJSON, _ := canonicalJSON(normalizePacketValue(right, ""))
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("nested set order changed canonical packet: %s != %s", leftJSON, rightJSON)
	}
}

func TestDeclarationIdentityIgnoresEquivalentMapAndSetOrder(t *testing.T) {
	left, err := declarationIdentity([]byte("spec:\n  effects:\n    allowed: [read-source, query-authority]\nmetadata:\n  name: synthetic\n"))
	if err != nil {
		t.Fatal(err)
	}
	right, err := declarationIdentity([]byte("metadata:\n  name: synthetic\nspec:\n  effects:\n    allowed: [query-authority, read-source]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("equivalent declaration identities differ: %s != %s", left, right)
	}
}

func TestCompileBindsFreshnessPolicyAndGitPayload(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	for index := range bundle.Observations {
		if bundle.Observations[index].Kind == "git" {
			bundle.Observations[index].Payload["repository"] = "different-repository"
		}
	}
	bundle, err = NewSourceBundle(bundle.DeclarationDigest, bundle.SourceCatalogDigest, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bundle)
	preview, err := CompileWithBundle(m, manifestPath, raw, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC))
	if err != nil || preview.Ready || preview.Packet != nil {
		t.Fatalf("mismatched Git preview=%#v err=%v", preview, err)
	}
}

func TestCompileRejectsSubstitutedFreshnessAndCallerScope(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "examples", "platoon-typed.yaml")
	m, err := manifest.LoadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	bundle.Observations[0].FreshnessPolicy = "max-age:5m"
	bundle, err = NewSourceBundle(bundle.DeclarationDigest, bundle.SourceCatalogDigest, bundle.CallerRole, bundle.QueryScope, bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bundle)
	preview, err := CompileWithBundle(m, manifestPath, raw, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC))
	if err != nil || preview.Ready {
		t.Fatalf("substituted freshness preview=%#v err=%v", preview, err)
	}

	bundle = bundleForManifest(t, m, manifestPath, "2026-08-08T10:00:00Z")
	bundle, err = NewSourceBundle(bundle.DeclarationDigest, bundle.SourceCatalogDigest, "operator", "entry-platoon", bundle.Observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(bundle)
	preview, err = CompileWithBundle(m, manifestPath, raw, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC))
	if err != nil || preview.Ready {
		t.Fatalf("cross-scope preview=%#v err=%v", preview, err)
	}
}

func bundleForManifest(t *testing.T, m *manifest.Manifest, manifestPath, observedAt string) SourceBundle {
	t.Helper()
	declarationPath := filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(m.Spec.Mission))
	raw, err := os.ReadFile(declarationPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	catalogDigest, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]SourceObservation, 0, len(d.Spec.Sources))
	for _, declared := range d.Spec.Sources {
		observations = append(observations, SourceObservation{
			SourceID: declared.ID, Kind: declared.Kind, Schema: declared.Schema, AdapterVersion: "v1", Revision: declared.Revision,
			Quality: QualityVerified, ObservedAt: observedAt, FreshnessPolicy: declared.ObservationPolicy,
			Payload: testPayload(declared.Kind, strings.Repeat("a", 64)),
		})
	}
	declarationDigest, err := declarationIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := NewSourceBundle(declarationDigest, catalogDigest, "operator", "entry-operator", observations)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func testPayload(kind, digest string) map[string]any {
	switch kind {
	case "git":
		return map[string]any{"repository": "synthetic-repository", "objectId": strings.Repeat("a", 40)}
	case "dagr":
		return map[string]any{"databaseIdentity": digest, "schemaVersion": "v1", "operations": []any{"ack", "list", "load", "start", "watch"}}
	case "sergeant", "td":
		return map[string]any{"resolutionNamespace": "synthetic", "sourceVersion": "v1", "evidenceDigest": digest}
	case "validation-capability":
		return map[string]any{"profileDigest": digest, "executableDigest": digest, "sandboxDigest": digest, "policyDigest": digest}
	case "platoon-policy":
		return map[string]any{"policyDigest": digest}
	default:
		return map[string]any{"status": "unsupported"}
	}
}
