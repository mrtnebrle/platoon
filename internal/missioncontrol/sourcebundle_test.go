package missioncontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSourceBundleIdentitySeparatesContentFromProvenance(t *testing.T) {
	first := testObservation("2026-08-08T10:00:00Z")
	second := testObservation("2026-08-08T10:01:00Z")

	left, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{first})
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{second})
	if err != nil {
		t.Fatal(err)
	}
	if left.Observations[0].ContentDigest != right.Observations[0].ContentDigest || left.ContentSetDigest != right.ContentSetDigest {
		t.Fatal("observation time changed content identity")
	}
	if left.Observations[0].EnvelopeDigest == right.Observations[0].EnvelopeDigest || left.BundleID == right.BundleID {
		t.Fatal("observation time did not change provenance identity")
	}
}

func TestSourceBundleCanonicalizesMapAndSetOrder(t *testing.T) {
	left := testObservation("2026-08-08T10:00:00Z")
	right := testObservation("2026-08-08T10:00:00Z")
	left.Payload = map[string]any{"databaseIdentity": "synthetic-database", "schemaVersion": "v1", "operations": []any{"watch", "load"}}
	right.Payload = map[string]any{"operations": []any{"load", "watch"}, "schemaVersion": "v1", "databaseIdentity": "synthetic-database"}

	first, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{left})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{right})
	if err != nil {
		t.Fatal(err)
	}
	if first.BundleID != second.BundleID || first.ContentSetDigest != second.ContentSetDigest {
		t.Fatalf("equivalent observations changed identity: %s != %s", first.BundleID, second.BundleID)
	}
}

func TestDecodeSourceBundleRejectsTamperingPrivacyAndStaleness(t *testing.T) {
	bundle, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{testObservation("2026-08-08T10:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["bundleId"] = strings.Repeat("0", 64)
	tampered, _ := json.Marshal(document)
	if _, err := DecodeSourceBundle(tampered, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("tampered bundle error = %v", err)
	}

	private := testObservation("2026-08-08T10:00:00Z")
	private.Payload = map[string]any{"rawBody": "/private/synthetic/source"}
	if _, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{private}); err == nil {
		t.Fatal("accepted raw private source body")
	}

	stale := testObservation("2026-08-08T10:00:00Z")
	stale.FreshnessPolicy = "max-age:30s"
	staleBundle, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry", []SourceObservation{stale})
	if err != nil {
		t.Fatal(err)
	}
	staleRaw, _ := json.Marshal(staleBundle)
	if _, err := DecodeSourceBundle(staleRaw, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale bundle error = %v", err)
	}
}

func TestCanonicalJSONUsesRFC8785Encoding(t *testing.T) {
	canonical, err := canonicalJSON(map[string]any{"line": "\u2028", "number": json.Number("1e+30"), "zero": json.Number("-0")})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != "{\"line\":\"\u2028\",\"number\":1e+30,\"zero\":0}" {
		t.Fatalf("canonical JSON = %s", canonical)
	}
}

func TestSourceBundleRejectsSecretValuesAndAggregateOverflow(t *testing.T) {
	secret := testObservation("2026-08-08T10:00:00Z")
	secret.Payload["databaseIdentity"] = "token=synthetic"
	if _, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry-operator", []SourceObservation{secret}); err == nil {
		t.Fatal("accepted secret-like source value")
	}

	large := make([]SourceObservation, 5)
	operations := make([]any, 130000)
	for index := range operations {
		operations[index] = "read"
	}
	for index := range large {
		large[index] = testObservation("2026-08-08T10:00:00Z")
		large[index].SourceID = "dagr-source-" + string(rune('a'+index))
		large[index].Payload["operations"] = operations
	}
	if _, err := NewSourceBundle("declaration-digest", "catalog-digest", "operator", "entry-operator", large); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate overflow error = %v", err)
	}
}

func testObservation(observedAt string) SourceObservation {
	return SourceObservation{
		SourceID: "dagr-authority", Kind: "dagr", Schema: "dagr.capability/v1", AdapterVersion: "v1",
		Revision: "capability-v1", Quality: QualityVerified, ObservedAt: observedAt,
		FreshnessPolicy: "max-age:5m", Payload: map[string]any{"databaseIdentity": "synthetic-database", "schemaVersion": "v1", "operations": []any{"load", "watch"}},
	}
}
