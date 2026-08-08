package missioncontrol

import (
	"context"
	"strings"
	"testing"
)

func TestSurveyQueriesOnlyDeclaredSourcesAfterGate(t *testing.T) {
	querier := &recordingSourceQuerier{observations: map[string]SourceObservation{
		"policy": {
			SourceID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", AdapterVersion: "v1",
			Revision: "v1", Quality: QualityVerified, ObservedAt: "2026-08-08T10:00:00Z", Payload: map[string]any{"policyDigest": strings.Repeat("a", 64)},
		},
	}}
	d := surveyDeclaration()
	bundle, err := surveyValidated(context.Background(), d, []byte("synthetic declaration"), "operator", "", SourceRegistry{ByKind: map[string]SourceQuerier{"platoon-policy": querier}})
	if err != nil {
		t.Fatal(err)
	}
	if len(querier.queries) != 1 || querier.queries[0].SourceID != "policy" || bundle.Observations[0].SourceID != "policy" {
		t.Fatalf("queries=%#v bundle=%#v", querier.queries, bundle)
	}
}

func TestSurveyGateRefusesBeforeQuery(t *testing.T) {
	querier := &recordingSourceQuerier{}
	d := surveyDeclaration()
	d.Spec.Effects.Callers["read-source"] = []string{"platoon"}
	_, err := surveyValidated(context.Background(), d, []byte("synthetic declaration"), "operator", "", SourceRegistry{ByKind: map[string]SourceQuerier{"platoon-policy": querier}})
	if err == nil || !strings.Contains(err.Error(), "caller") {
		t.Fatalf("survey error = %v", err)
	}
	if len(querier.queries) != 0 {
		t.Fatalf("gate failure reached adapter: %#v", querier.queries)
	}
}

func TestSurveyRoutesTDOnlyThroughSergeantMissionSource(t *testing.T) {
	generic := &recordingSourceQuerier{}
	d := surveyDeclaration()
	d.Spec.Sources[0] = source{ID: "work", Kind: "td", Schema: "sergeant.td-observation/v1", Locator: "synthetic-work", ObservationPolicy: "max-age:5m", Role: "work", Reason: "Tracks synthetic work."}
	bundle, err := surveyValidated(context.Background(), d, []byte("synthetic declaration"), "operator", "", SourceRegistry{ByKind: map[string]SourceQuerier{"td": generic}})
	if err != nil {
		t.Fatal(err)
	}
	if len(generic.queries) != 0 || bundle.Observations[0].Quality != QualityUnsupported {
		t.Fatalf("generic queries=%#v observation=%#v", generic.queries, bundle.Observations[0])
	}
}

type recordingSourceQuerier struct {
	queries      []SourceQuery
	observations map[string]SourceObservation
}

func (q *recordingSourceQuerier) Query(_ context.Context, query SourceQuery) (SourceObservation, error) {
	q.queries = append(q.queries, query)
	return q.observations[query.SourceID], nil
}

func surveyDeclaration() *declaration {
	return &declaration{Spec: missionSpec{
		Effects: &effects{
			Allowed: []string{"read-source"}, Prohibited: []string{}, Stages: map[string][]string{},
			Callers: map[string][]string{"read-source": {"operator"}},
		},
		Sources: []source{{ID: "policy", Kind: "platoon-policy", Schema: "platoon.policy/v1alpha1", Locator: "public-policy", Revision: "v1", Role: "policy", Reason: "Defines synthetic policy."}},
		Stops:   []stop{}, Unknowns: []unknown{}, Contradictions: []contradiction{},
	}}
}
