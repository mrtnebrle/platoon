package planner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

type Status string

const (
	Admitted Status = "admitted"
	Blocked  Status = "blocked"
)

type Decision struct {
	Stage        string `json:"stage"`
	Ready        bool   `json:"ready"`
	Status       Status `json:"status"`
	Reason       string `json:"reason"`
	CriticalPath int    `json:"criticalPath"`
	Unlocks      int    `json:"unlocks"`
}

type ActiveClaim struct {
	Stage          string        `json:"stage"`
	Repository     string        `json:"repository"`
	Mode           manifest.Mode `json:"mode"`
	Paths          []string      `json:"paths,omitempty"`
	Semantic       []string      `json:"semantic,omitempty"`
	TokenReleased  bool          `json:"tokenReleased,omitempty"`
	ClaimsReleased bool          `json:"claimsReleased,omitempty"`
}

type Priority struct {
	Stage        string
	CriticalPath int
	Unlocks      int
}

func Plan(m *manifest.Manifest, existing []ActiveClaim) []Decision {
	priorities := Priorities(m)
	priorityByStage := make(map[string]Priority, len(priorities))
	for _, priority := range priorities {
		priorityByStage[priority.Stage] = priority
	}

	active := append([]ActiveClaim(nil), existing...)
	adoptionDecisions := make(map[string]Decision)
	for _, priority := range priorities {
		stage, _ := m.Stage(priority.Stage)
		if stage.AdoptFleet == "" {
			continue
		}
		status := Admitted
		reason := "declared adoption reserves capacity pending verification"
		if !activeContains(active, stage.ID) {
			if admitted, blockedReason := CanAdmit(m, stage, active); !admitted {
				status = Blocked
				reason = "declared adoption conflicts: " + blockedReason
			}
		}
		adoptionDecisions[stage.ID] = Decision{
			Stage: stage.ID, Ready: len(stage.DependsOn) == 0, Status: status, Reason: reason,
			CriticalPath: priority.CriticalPath, Unlocks: priority.Unlocks,
		}
		if activeContains(active, stage.ID) {
			continue
		}
		active = append(active, ActiveClaim{
			Stage: stage.ID, Repository: stage.Repository, Mode: stage.Mode,
			Paths: append([]string(nil), stage.Claims.Paths...), Semantic: append([]string(nil), stage.Claims.Semantic...),
		})
	}
	decisions := make([]Decision, 0, len(m.Spec.Stages))
	for _, priority := range priorities {
		stage, _ := m.Stage(priority.Stage)
		if len(stage.DependsOn) != 0 {
			continue
		}
		if stage.AdoptFleet != "" {
			decisions = append(decisions, adoptionDecisions[stage.ID])
			continue
		}
		admitted, reason := CanAdmit(m, stage, active)
		status := Blocked
		if admitted {
			status = Admitted
			active = append(active, ActiveClaim{
				Stage: stage.ID, Repository: stage.Repository, Mode: stage.Mode,
				Paths: append([]string(nil), stage.Claims.Paths...), Semantic: append([]string(nil), stage.Claims.Semantic...),
			})
		}
		decisions = append(decisions, Decision{
			Stage: stage.ID, Ready: true, Status: status, Reason: reason,
			CriticalPath: priority.CriticalPath, Unlocks: priority.Unlocks,
		})
	}
	for _, priority := range priorities {
		stage, _ := m.Stage(priority.Stage)
		if len(stage.DependsOn) == 0 {
			continue
		}
		if stage.AdoptFleet != "" {
			decisions = append(decisions, adoptionDecisions[stage.ID])
			continue
		}
		dependencies := append([]string(nil), stage.DependsOn...)
		sort.Strings(dependencies)
		decisions = append(decisions, Decision{
			Stage: stage.ID, Ready: false, Status: Blocked,
			Reason:       "waiting for " + strings.Join(dependencies, ", "),
			CriticalPath: priorityByStage[stage.ID].CriticalPath,
			Unlocks:      priorityByStage[stage.ID].Unlocks,
		})
	}
	return decisions
}

func activeContains(active []ActiveClaim, stage string) bool {
	for _, claim := range active {
		if claim.Stage == stage {
			return true
		}
	}
	return false
}

func Priorities(m *manifest.Manifest) []Priority {
	children := make(map[string][]string, len(m.Spec.Stages))
	for _, stage := range m.Spec.Stages {
		for _, dependency := range stage.DependsOn {
			children[dependency] = append(children[dependency], stage.ID)
		}
	}
	for id := range children {
		sort.Strings(children[id])
	}
	memo := map[string]int{}
	var criticalPath func(string) int
	criticalPath = func(id string) int {
		if value := memo[id]; value != 0 {
			return value
		}
		longest := 1
		for _, child := range children[id] {
			if candidate := 1 + criticalPath(child); candidate > longest {
				longest = candidate
			}
		}
		memo[id] = longest
		return longest
	}

	result := make([]Priority, 0, len(m.Spec.Stages))
	for _, stage := range m.Spec.Stages {
		result = append(result, Priority{Stage: stage.ID, CriticalPath: criticalPath(stage.ID), Unlocks: len(children[stage.ID])})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CriticalPath != result[j].CriticalPath {
			return result[i].CriticalPath > result[j].CriticalPath
		}
		if result[i].Unlocks != result[j].Unlocks {
			return result[i].Unlocks > result[j].Unlocks
		}
		return result[i].Stage < result[j].Stage
	})
	return result
}

func CanAdmit(m *manifest.Manifest, stage manifest.Stage, active []ActiveClaim) (bool, string) {
	implementation, review := 0, 0
	for _, claim := range active {
		if claim.TokenReleased {
			continue
		}
		switch claim.Mode {
		case manifest.Implementation:
			implementation++
		case manifest.Review:
			review++
		}
	}
	if stage.Mode == manifest.Implementation && implementation >= m.Spec.Limits.Implementation {
		return false, "implementation token limit reached"
	}
	if stage.Mode == manifest.Review && review >= m.Spec.Limits.Review {
		return false, "review token limit reached"
	}
	if stage.Mode == manifest.Review {
		return true, "review token available"
	}

	repository, ok := m.Repository(stage.Repository)
	if !ok {
		return false, "repository policy unavailable"
	}
	writers := 0
	for _, claim := range active {
		if claim.Repository == stage.Repository && claim.Mode == manifest.Implementation && !claim.ClaimsReleased {
			writers++
		}
	}
	if writers >= repository.MaxWriters {
		return false, "repository writer limit reached"
	}
	for _, claim := range active {
		if claim.Repository != stage.Repository || claim.Mode != manifest.Implementation || claim.ClaimsReleased {
			continue
		}
		candidate := ActiveClaim{
			Stage: stage.ID, Repository: stage.Repository, Mode: stage.Mode,
			Paths: stage.Claims.Paths, Semantic: stage.Claims.Semantic,
		}
		if conflict, reason := ActiveClaimsConflict(candidate, claim); conflict {
			return false, reason
		}
	}
	return true, "implementation token and claims available"
}

func ActiveClaimsConflict(left, right ActiveClaim) (bool, string) {
	if left.Repository != right.Repository || left.Mode != manifest.Implementation || right.Mode != manifest.Implementation ||
		left.ClaimsReleased || right.ClaimsReleased {
		return false, ""
	}
	if hasProtected(left.Semantic) || hasProtected(right.Semantic) {
		return true, "protected semantic claim requires repository exclusivity"
	}
	for _, leftPath := range left.Paths {
		for _, rightPath := range right.Paths {
			if PathsOverlap(leftPath, rightPath) {
				return true, fmt.Sprintf("path claim overlaps active stage %s", right.Stage)
			}
		}
	}
	for _, leftSemantic := range left.Semantic {
		for _, rightSemantic := range right.Semantic {
			leftNormalized, _ := manifest.NormalizeSemantic(leftSemantic)
			rightNormalized, _ := manifest.NormalizeSemantic(rightSemantic)
			if leftNormalized == rightNormalized {
				return true, fmt.Sprintf("semantic claim overlaps active stage %s", right.Stage)
			}
		}
	}
	return false, ""
}

func PathsOverlap(left, right string) bool {
	left = strings.ToLower(left)
	right = strings.ToLower(right)
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func CoversPath(claim, changed string) bool {
	return claim == changed || strings.HasPrefix(changed, claim+"/")
}

func IsProtectedSemantic(value string) bool {
	normalized, err := manifest.NormalizeSemantic(value)
	if err != nil {
		return true
	}
	for protected := range protectedSemantic {
		if normalized == protected || strings.HasPrefix(normalized, protected+"-") ||
			strings.HasSuffix(normalized, "-"+protected) || strings.Contains(normalized, "-"+protected+"-") {
			return true
		}
	}
	return false
}

func hasProtected(values []string) bool {
	for _, value := range values {
		if IsProtectedSemantic(value) {
			return true
		}
	}
	return false
}

var protectedSemantic = map[string]struct{}{
	"migration":              {},
	"migrations":             {},
	"state-machine":          {},
	"state-machines":         {},
	"authorization":          {},
	"identity":               {},
	"recovery":               {},
	"purge":                  {},
	"release":                {},
	"destructive":            {},
	"destructive-behavior":   {},
	"repository-integration": {},
}
