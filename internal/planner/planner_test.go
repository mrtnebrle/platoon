package planner_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/planner"
)

func TestPlanUsesDeterministicCriticalPathOrdering(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 1
	m.Spec.Repositories[0].MaxWriters = 3
	m.Spec.Stages = []manifest.Stage{
		stage("short", "repo", nil, []string{"short-path"}, []string{"short-contract"}),
		stage("critical", "repo", nil, []string{"critical-path"}, []string{"critical-contract"}),
		stage("critical-child", "repo", []string{"critical"}, []string{"child-path"}, []string{"child-contract"}),
	}

	got := planner.Plan(m, nil)
	want := []planner.Decision{
		{Stage: "critical", Ready: true, Status: planner.Admitted, Reason: "implementation token and claims available", CriticalPath: 2, Unlocks: 1},
		{Stage: "short", Ready: true, Status: planner.Blocked, Reason: "implementation token limit reached", CriticalPath: 1, Unlocks: 0},
		{Stage: "critical-child", Ready: false, Status: planner.Blocked, Reason: "waiting for critical", CriticalPath: 1, Unlocks: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %#v\nwant %#v", got, want)
	}

	for i := 0; i < 20; i++ {
		if next := planner.Plan(m, nil); !reflect.DeepEqual(next, got) {
			t.Fatalf("Plan() is nondeterministic: %#v then %#v", got, next)
		}
	}
}

func TestPlanAllowsOnlyDisjointSameRepositoryWriters(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 4
	m.Spec.Repositories[0].MaxWriters = 4
	m.Spec.Stages = []manifest.Stage{
		stage("api", "repo", nil, []string{"internal/api"}, []string{"api-contract"}),
		stage("worker", "repo", nil, []string{"internal/worker"}, []string{"worker-contract"}),
		stage("nested", "repo", nil, []string{"internal/api/routes"}, []string{"routes"}),
		stage("protected", "repo", nil, []string{"docs"}, []string{"State_Machines"}),
	}

	got := planner.Plan(m, nil)
	statuses := map[string]planner.Decision{}
	for _, decision := range got {
		statuses[decision.Stage] = decision
	}
	if statuses["api"].Status != planner.Admitted || statuses["worker"].Status != planner.Admitted {
		t.Fatalf("disjoint writers were not admitted: %#v", got)
	}
	if statuses["nested"].Status != planner.Blocked || statuses["nested"].Reason != "path claim overlaps active stage api" {
		t.Fatalf("nested overlap = %#v", statuses["nested"])
	}
	if statuses["protected"].Status != planner.Blocked || statuses["protected"].Reason != "protected semantic claim requires repository exclusivity" {
		t.Fatalf("protected overlap = %#v", statuses["protected"])
	}
}

func TestPlanAccountsForAdoptedWork(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 1
	m.Spec.Stages = []manifest.Stage{
		stage("next", "repo", nil, []string{"internal/next"}, []string{"next-contract"}),
	}
	active := []planner.ActiveClaim{{Stage: "adopted", Repository: "other", Mode: manifest.Implementation}}

	got := planner.Plan(m, active)
	if got[0].Status != planner.Blocked || got[0].Reason != "implementation token limit reached" {
		t.Fatalf("Plan() = %#v, want adopted work to consume token", got)
	}
}

func TestPlanConservativelyAccountsForDeclaredAdoption(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 1
	adopted := stage("adopted", "repo", nil, []string{"internal/adopted"}, []string{"adopted-contract"})
	adopted.AdoptFleet = "existing-fleet"
	m.Spec.Stages = []manifest.Stage{
		adopted,
		stage("next", "repo", nil, []string{"internal/next"}, []string{"next-contract"}),
	}
	got := planner.Plan(m, nil)
	byStage := map[string]planner.Decision{}
	for _, decision := range got {
		byStage[decision.Stage] = decision
	}
	if byStage["adopted"].Status != planner.Admitted || byStage["adopted"].Reason != "declared adoption reserves capacity pending verification" {
		t.Fatalf("adopted decision = %#v", byStage["adopted"])
	}
	if byStage["next"].Reason != "implementation token limit reached" {
		t.Fatalf("next decision = %#v", byStage["next"])
	}
}

func TestClaimsArePortableAcrossCaseInsensitiveFilesystems(t *testing.T) {
	if !planner.PathsOverlap("Internal/API", "internal/api/routes") {
		t.Fatal("case variation bypassed ancestor path overlap")
	}
	if planner.CoversPath("Internal/API", "internal/api/handler.go") {
		t.Fatal("case-folded coverage authorized a distinct case-sensitive path")
	}
	if !planner.IsProtectedSemantic("Authorization_Policy") {
		t.Fatal("qualified authorization claim bypassed protected exclusivity")
	}
}

func TestPlanBlocksConflictingDeclaredAdoptionsButStillAccountsForThem(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 2
	m.Spec.Repositories[0].MaxWriters = 2
	first := stage("adopt-a", "repo", nil, []string{"internal/shared"}, []string{"shared-contract"})
	first.AdoptFleet = "fleet-a"
	second := stage("adopt-b", "repo", nil, []string{"internal/shared/child"}, []string{"other-contract"})
	second.AdoptFleet = "fleet-b"
	next := stage("next", "repo", nil, []string{"internal/next"}, []string{"next-contract"})
	m.Spec.Stages = []manifest.Stage{second, next, first}
	decisions := planner.Plan(m, nil)
	byStage := map[string]planner.Decision{}
	for _, decision := range decisions {
		byStage[decision.Stage] = decision
	}
	if byStage["adopt-a"].Status != planner.Admitted {
		t.Fatalf("first adoption = %#v", byStage["adopt-a"])
	}
	if byStage["adopt-b"].Status != planner.Blocked || !strings.Contains(byStage["adopt-b"].Reason, "path claim overlaps") {
		t.Fatalf("conflicting adoption = %#v", byStage["adopt-b"])
	}
	if byStage["next"].Reason != "implementation token limit reached" {
		t.Fatalf("later stage did not account for blocked adoption: %#v", byStage["next"])
	}
}

func TestReviewStagesUseAnIndependentTokenPool(t *testing.T) {
	m := testManifest()
	m.Spec.Limits.Implementation = 1
	m.Spec.Limits.Review = 1
	implementation := stage("implementation", "repo", nil, []string{"internal/api"}, []string{"api-contract"})
	reviewA := manifest.Stage{ID: "review-a", Repository: "repo", Mode: manifest.Review}
	reviewB := manifest.Stage{ID: "review-b", Repository: "repo", Mode: manifest.Review}
	m.Spec.Stages = []manifest.Stage{implementation, reviewA, reviewB}
	decisions := planner.Plan(m, nil)
	byStage := map[string]planner.Decision{}
	for _, decision := range decisions {
		byStage[decision.Stage] = decision
	}
	if byStage["implementation"].Status != planner.Admitted || byStage["review-a"].Status != planner.Admitted {
		t.Fatalf("independent pools did not admit implementation and review: %#v", decisions)
	}
	if byStage["review-b"].Reason != "review token limit reached" {
		t.Fatalf("second review = %#v", byStage["review-b"])
	}
}

func testManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Spec: manifest.Spec{
			Limits:       manifest.Limits{Implementation: 6, Review: 2},
			Repositories: []manifest.Repository{{ID: "repo", MaxWriters: 1}},
		},
	}
}

func stage(id, repo string, deps, paths, semantic []string) manifest.Stage {
	return manifest.Stage{
		ID:         id,
		Repository: repo,
		Mode:       manifest.Implementation,
		DependsOn:  deps,
		Claims:     manifest.Claims{Paths: paths, Semantic: semantic},
	}
}
