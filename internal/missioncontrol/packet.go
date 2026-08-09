package missioncontrol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"gopkg.in/yaml.v3"
)

const PacketSchema = "platoon.packet/v1alpha1"

type PacketPreview struct {
	Schema                  string `json:"schema"`
	ID                      string `json:"id"`
	ManifestSourceDigest    string `json:"manifestSourceDigest"`
	DeclarationSourceDigest string `json:"declarationSourceDigest"`
	IntentRevision          string `json:"intentRevision"`
	IntentMediaType         string `json:"intentMediaType"`
	BundleID                string `json:"bundleId"`
	ContentSetDigest        string `json:"contentSetDigest"`
	compiled                *compiledPacket
}

func CompileWithBundle(m *manifest.Manifest, manifestFile string, bundleBytes []byte, evaluatedAt time.Time) (Preview, error) {
	return compileWithBundle(m, manifestFile, bundleBytes, evaluatedAt, true)
}

func CompileForApply(m *manifest.Manifest, manifestFile string, bundleBytes []byte) (Preview, error) {
	return compileWithBundle(m, manifestFile, bundleBytes, time.Time{}, false)
}

func compileWithBundle(m *manifest.Manifest, manifestFile string, bundleBytes []byte, evaluatedAt time.Time, checkFreshness bool) (Preview, error) {
	if m.Spec.MissionFormat != manifest.MissionDeclarationV1Alpha1 {
		return Compile(m, manifestFile)
	}
	declarationBytes, err := readStable(declarationFile(m, manifestFile))
	if err != nil {
		return Preview{}, compileFailure(classifyReadError(err))
	}
	d, err := decode(declarationBytes)
	if err != nil {
		return Preview{}, compileFailure(classifyDecodeError(err))
	}
	if err := validate(d, m); err != nil {
		return Preview{}, compileFailure(classifyValidationError(err))
	}
	preview := previewDeclaration(d, m)
	var bundle SourceBundle
	if checkFreshness {
		bundle, err = DecodeSourceBundle(bundleBytes, evaluatedAt)
	} else {
		bundle, err = decodeSourceBundleIdentity(bundleBytes)
	}
	if err != nil {
		return notReadyPreview(preview, bundleReason(err)), nil
	}
	catalogDigest, err := sourceCatalogDigest(d.Spec.Sources)
	if err != nil {
		return Preview{}, compileFailure("invalid-source")
	}
	declarationDigest, err := declarationIdentity(declarationBytes)
	if err != nil || bundle.DeclarationDigest != declarationDigest || bundle.SourceCatalogDigest != catalogDigest {
		return notReadyPreview(preview, "source bundle does not match the declaration"), nil
	}
	if !bundleScopeValid(bundle.CallerRole, bundle.QueryScope) {
		return notReadyPreview(preview, "source bundle caller provenance is invalid"), nil
	}

	observed := make(map[string]SourceObservation, len(bundle.Observations))
	for _, observation := range bundle.Observations {
		observed[observation.SourceID] = observation
	}
	reasons := retainedSufficiencyReasons(preview.Sufficiency)
	for _, declared := range d.Spec.Sources {
		observation, ok := observed[declared.ID]
		if !ok || observation.Kind != declared.Kind || observation.Schema != declared.Schema ||
			observation.FreshnessPolicy != declared.ObservationPolicy || !sourceMatchesDeclaration(declared, observation) {
			reasons = append(reasons, "source "+declared.ID+" is absent or mismatched")
			continue
		}
		if observation.Quality != QualityVerified {
			reasons = append(reasons, "source "+declared.ID+" is "+string(observation.Quality))
		}
	}
	if len(observed) != len(d.Spec.Sources) {
		reasons = append(reasons, "source bundle contains undeclared observations")
	}
	for _, stop := range d.Spec.Stops {
		if !stop.Scope.Entry {
			continue
		}
		observation, ok := observed[stop.Predicate.Source]
		if !ok || observation.Quality != QualityVerified || stopMatches(stop.Predicate, observation) {
			reasons = append(reasons, "entry stop "+stop.ID+" is active or inconclusive")
		}
	}
	if len(reasons) != 0 {
		sort.Strings(reasons)
		preview.Ready = false
		preview.Sufficiency = decisionsForReasons(reasons)
		preview.Packet = nil
		return preview, nil
	}

	manifestBytes, err := readStable(manifestFile)
	if err != nil {
		return Preview{}, compileFailure("manifest-changed")
	}
	current, err := manifest.Load(manifestBytes)
	if err != nil || !sameManifest(current, m) {
		return Preview{}, compileFailure("manifest-changed")
	}
	intentPath := filepath.Join(filepath.Dir(manifestFile), filepath.FromSlash(m.Spec.Intent))
	intentBytes, err := readStable(intentPath)
	if err != nil {
		return Preview{}, compileFailure("intent-unavailable")
	}
	packet, err := compilePacket(m, declarationBytes, d, manifestBytes, intentBytes, bundle)
	if err != nil {
		return Preview{}, compileFailure("packet-canonicalization")
	}
	preview.Packet = &packet
	return preview, nil
}

func declarationIdentity(raw []byte) (string, error) {
	value, err := yamlValue(raw)
	if err != nil {
		return "", err
	}
	return canonicalDigest("platoon.normalized-declaration/v1alpha1", normalizePacketValue(value, ""))
}

func compilePacket(m *manifest.Manifest, declarationBytes []byte, d *declaration, manifestBytes, intentBytes []byte, bundle SourceBundle) (PacketPreview, error) {
	manifestValue, err := jsonValue(m)
	if err != nil {
		return PacketPreview{}, err
	}
	declarationValue, err := yamlValue(declarationBytes)
	if err != nil {
		return PacketPreview{}, err
	}
	manifestValue = normalizePacketValue(manifestValue, "")
	declarationValue = normalizePacketValue(declarationValue, "")
	declarationMap := declarationValue.(map[string]any)
	spec := declarationMap["spec"].(map[string]any)
	mediaType := "application/octet-stream"
	if strings.EqualFold(filepath.Ext(m.Spec.Intent), ".md") {
		mediaType = "text/markdown"
	}
	packet := PacketPreview{
		Schema: PacketSchema, ManifestSourceDigest: sha256Hex(manifestBytes), DeclarationSourceDigest: sha256Hex(declarationBytes),
		IntentRevision: sha256Hex(intentBytes), IntentMediaType: mediaType, BundleID: bundle.BundleID, ContentSetDigest: bundle.ContentSetDigest,
	}
	manifestDigest, err := canonicalDigest("platoon.normalized-manifest/v1alpha1", manifestValue)
	if err != nil {
		return PacketPreview{}, err
	}
	declarationDigest, err := canonicalDigest("platoon.normalized-declaration/v1alpha1", declarationValue)
	if err != nil {
		return PacketPreview{}, err
	}
	envelope := map[string]any{
		"schema": PacketSchema, "manifest": manifestValue, "declaration": declarationValue,
		"manifestDigest": manifestDigest, "declarationDigest": declarationDigest,
		"manifestSourceDigest": packet.ManifestSourceDigest, "declarationSourceDigest": packet.DeclarationSourceDigest,
		"intentRevision": packet.IntentRevision, "intentMediaType": packet.IntentMediaType,
		"handoffs": spec["handoffs"], "sources": spec["sources"], "bundleId": packet.BundleID, "contentSetDigest": packet.ContentSetDigest,
	}
	packet.ID, err = canonicalDigest(PacketSchema, envelope)
	if err == nil {
		encoded, encodeErr := canonicalJSON(envelope)
		if encodeErr != nil {
			return PacketPreview{}, encodeErr
		}
		packet.compiled = &compiledPacket{
			Envelope:     encoded,
			Sources:      append([]source(nil), d.Spec.Sources...),
			Observations: append([]SourceObservation(nil), bundle.Observations...),
		}
	}
	return packet, err
}

func sourceMatchesDeclaration(declared source, observation SourceObservation) bool {
	if declared.Revision != "" && observation.Revision != declared.Revision {
		return false
	}
	if observation.Quality != QualityVerified {
		return true
	}
	if declared.Kind == "git" {
		repository, repositoryOK := observation.Payload["repository"].(string)
		objectID, objectOK := observation.Payload["objectId"].(string)
		return repositoryOK && objectOK && repository == declared.Locator && objectID == declared.Revision
	}
	return true
}

func bundleScopeValid(callerRole, queryScope string) bool {
	if callerRole != "stage" {
		return queryScope == surveyQueryScope(callerRole, "")
	}
	return strings.HasPrefix(queryScope, "stage-") && strings.HasSuffix(queryScope, "-stage") && len(queryScope) > len("stage--stage")
}

func sourceCatalogDigest(sources []source) (string, error) {
	descriptors := make([]any, 0, len(sources))
	for _, declared := range sources {
		descriptors = append(descriptors, map[string]any{
			"id": declared.ID, "kind": declared.Kind, "schema": declared.Schema, "locator": declared.Locator,
			"revision": declared.Revision, "observationPolicy": declared.ObservationPolicy, "role": declared.Role, "reason": declared.Reason,
		})
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].(map[string]any)["id"].(string) < descriptors[j].(map[string]any)["id"].(string)
	})
	return canonicalDigest("platoon.source-catalog/v1alpha1", descriptors)
}

func notReadyPreview(preview Preview, reason string) Preview {
	preview.Ready = false
	preview.Packet = nil
	preview.Sufficiency = []SufficiencyDecision{{Status: "not-ready", Reason: reason}}
	return preview
}

func bundleReason(err error) string {
	switch {
	case strings.Contains(err.Error(), "stale"):
		return "source bundle is stale"
	case strings.Contains(err.Error(), "unsupported"):
		return "source bundle schema is unsupported"
	default:
		return "source bundle is invalid"
	}
}

func retainedSufficiencyReasons(decisions []SufficiencyDecision) []string {
	var reasons []string
	for _, decision := range decisions {
		if decision.Status == "ready" || strings.HasPrefix(decision.Reason, "source ") || strings.HasPrefix(decision.Reason, "entry stop ") {
			continue
		}
		reasons = append(reasons, decision.Reason)
	}
	return reasons
}

func decisionsForReasons(reasons []string) []SufficiencyDecision {
	result := make([]SufficiencyDecision, 0, len(reasons))
	for _, reason := range reasons {
		result = append(result, SufficiencyDecision{Status: "not-ready", Reason: reason})
	}
	return result
}

func stopMatches(predicate stopPredicate, observation SourceObservation) bool {
	var current any
	components := strings.Split(predicate.Field, ".")
	switch components[0] {
	case "quality":
		current = string(observation.Quality)
	case "revision":
		current = observation.Revision
	case "observedAt":
		current = observation.ObservedAt
	default:
		current = observation.Payload
	}
	if components[0] == "quality" || components[0] == "revision" || components[0] == "observedAt" {
		components = components[1:]
	}
	for _, component := range components {
		object, ok := current.(map[string]any)
		if !ok {
			return true
		}
		current, ok = object[component]
		if !ok {
			return predicate.Operator != "exists" || predicate.Value != nil
		}
	}
	switch predicate.Operator {
	case "exists":
		return true
	case "equals", "quality_is":
		return fmt.Sprint(current) == fmt.Sprint(predicate.Value)
	case "not_equals":
		return fmt.Sprint(current) != fmt.Sprint(predicate.Value)
	case "in":
		values, _ := predicate.Value.([]any)
		for _, value := range values {
			if fmt.Sprint(current) == fmt.Sprint(value) {
				return true
			}
		}
	}
	return true
}

func sameManifest(left, right *manifest.Manifest) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func jsonValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return parseStrictJSON(raw)
}

func yamlValue(raw []byte) (any, error) {
	var value any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func normalizePacketValue(value any, path string) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			result[key] = normalizePacketValue(child, childPath)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			result[index] = normalizePacketValue(child, path+"[]")
		}
		if packetSetPath(path) {
			sort.Slice(result, func(i, j int) bool {
				left, _ := canonicalJSON(result[i])
				right, _ := canonicalJSON(result[j])
				return bytes.Compare(left, right) < 0
			})
		} else if len(result) > 0 && objectsHaveID(result) {
			sort.Slice(result, func(i, j int) bool {
				return result[i].(map[string]any)["id"].(string) < result[j].(map[string]any)["id"].(string)
			})
		}
		return result
	default:
		return current
	}
}

func packetSetPath(path string) bool {
	field := path
	if index := strings.LastIndexByte(path, '.'); index >= 0 {
		field = path[index+1:]
	}
	switch field {
	case "allowed", "prohibited", "effects", "callers", "evidenceRoles", "attemptedSources", "sources", "dependsOn", "paths", "semantic", "operations":
		return true
	default:
		return strings.Contains(path, "effects.callers.") || strings.Contains(path, "effects.stages.")
	}
}

func objectsHaveID(values []any) bool {
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := object["id"].(string); !ok {
			return false
		}
	}
	return true
}
