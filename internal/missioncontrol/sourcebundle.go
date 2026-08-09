package missioncontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

const (
	SourceBundleSchema = "platoon.source-bundle/v1alpha1"
	maxObservationSize = 1 << 20
	maxBundleSize      = 4 << 20
)

var (
	windowsAbsolutePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	secretValuePattern  = regexp.MustCompile(`(?i)(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{20,}|AKIA[A-Z0-9]{16})`)
)

type SourceQuality string

const (
	QualityVerified     SourceQuality = "verified"
	QualityInconclusive SourceQuality = "inconclusive"
	QualityUnavailable  SourceQuality = "unavailable"
	QualityUnsupported  SourceQuality = "unsupported"
)

type SourceObservation struct {
	SourceID        string         `json:"sourceId"`
	Kind            string         `json:"kind"`
	Schema          string         `json:"schema"`
	AdapterVersion  string         `json:"adapterVersion"`
	Revision        string         `json:"revision"`
	Quality         SourceQuality  `json:"quality"`
	ObservedAt      string         `json:"observedAt"`
	FreshnessPolicy string         `json:"freshnessPolicy,omitempty"`
	Payload         map[string]any `json:"payload"`
	ContentDigest   string         `json:"contentDigest"`
	EnvelopeDigest  string         `json:"envelopeDigest"`
}

type SourceBundle struct {
	Schema              string              `json:"schema"`
	BundleID            string              `json:"bundleId,omitempty"`
	DeclarationDigest   string              `json:"declarationDigest"`
	SourceCatalogDigest string              `json:"sourceCatalogDigest"`
	CallerRole          string              `json:"callerRole"`
	QueryScope          string              `json:"queryScope"`
	Observations        []SourceObservation `json:"observations"`
	ContentSetDigest    string              `json:"contentSetDigest"`
}

func NewSourceBundle(declarationDigest, catalogDigest, callerRole, queryScope string, observations []SourceObservation) (SourceBundle, error) {
	if declarationDigest == "" || catalogDigest == "" || !callerRoles[callerRole] || !safeOpaque(queryScope) || len(observations) == 0 || len(observations) > 1024 {
		return SourceBundle{}, errors.New("source bundle metadata is invalid")
	}
	result := SourceBundle{
		Schema: SourceBundleSchema, DeclarationDigest: declarationDigest, SourceCatalogDigest: catalogDigest,
		CallerRole: callerRole, QueryScope: queryScope, Observations: append([]SourceObservation(nil), observations...),
	}
	sort.Slice(result.Observations, func(i, j int) bool { return result.Observations[i].SourceID < result.Observations[j].SourceID })
	contentSet := make([]any, 0, len(result.Observations))
	for index := range result.Observations {
		observation := &result.Observations[index]
		if index > 0 && result.Observations[index-1].SourceID == observation.SourceID {
			return SourceBundle{}, errors.New("source bundle repeats a source")
		}
		if err := normalizeObservation(observation); err != nil {
			return SourceBundle{}, err
		}
		contentEnvelope := map[string]any{
			"adapterVersion": observation.AdapterVersion, "kind": observation.Kind, "payload": observation.Payload,
			"quality": string(observation.Quality), "revision": observation.Revision, "schema": observation.Schema, "sourceId": observation.SourceID,
		}
		observation.ContentDigest, _ = canonicalDigest("platoon.source-content/v1alpha1", contentEnvelope)
		envelope := map[string]any{
			"contentDigest": observation.ContentDigest, "freshnessPolicy": observation.FreshnessPolicy, "observedAt": observation.ObservedAt,
		}
		observation.EnvelopeDigest, _ = canonicalDigest("platoon.source-envelope/v1alpha1", envelope)
		contentSet = append(contentSet, map[string]any{"contentDigest": observation.ContentDigest, "sourceId": observation.SourceID})
	}
	result.ContentSetDigest, _ = canonicalDigest("platoon.source-content-set/v1alpha1", contentSet)
	result.BundleID, _ = canonicalDigest(SourceBundleSchema, result)
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxBundleSize {
		return SourceBundle{}, errors.New("source bundle exceeds its aggregate size limit")
	}
	return result, nil
}

func DecodeSourceBundle(raw []byte, evaluatedAt time.Time) (SourceBundle, error) {
	return decodeSourceBundle(raw, evaluatedAt, true)
}

func decodeSourceBundleIdentity(raw []byte) (SourceBundle, error) {
	return decodeSourceBundle(raw, time.Time{}, false)
}

func decodeSourceBundle(raw []byte, evaluatedAt time.Time, checkFreshness bool) (SourceBundle, error) {
	if len(raw) == 0 || len(raw) > maxBundleSize {
		return SourceBundle{}, errors.New("source bundle size is invalid")
	}
	if _, err := parseStrictJSON(raw); err != nil {
		return SourceBundle{}, errors.New("source bundle JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var supplied SourceBundle
	if err := decoder.Decode(&supplied); err != nil || supplied.Schema != SourceBundleSchema {
		return SourceBundle{}, errors.New("source bundle schema is unsupported")
	}
	rebuilt, err := NewSourceBundle(supplied.DeclarationDigest, supplied.SourceCatalogDigest, supplied.CallerRole, supplied.QueryScope, supplied.Observations)
	if err != nil {
		return SourceBundle{}, err
	}
	if supplied.BundleID != rebuilt.BundleID || supplied.ContentSetDigest != rebuilt.ContentSetDigest || len(supplied.Observations) != len(rebuilt.Observations) {
		return SourceBundle{}, errors.New("source bundle identity does not match its content")
	}
	for index := range supplied.Observations {
		if supplied.Observations[index].SourceID != rebuilt.Observations[index].SourceID ||
			supplied.Observations[index].ContentDigest != rebuilt.Observations[index].ContentDigest ||
			supplied.Observations[index].EnvelopeDigest != rebuilt.Observations[index].EnvelopeDigest {
			return SourceBundle{}, errors.New("source observation identity does not match its content")
		}
		if checkFreshness {
			if err := observationFresh(rebuilt.Observations[index], evaluatedAt); err != nil {
				return SourceBundle{}, err
			}
		}
	}
	return rebuilt, nil
}

func ReadSourceBundleFile(file string) ([]byte, error) {
	first, err := readBoundedBundle(file)
	if err != nil {
		return nil, err
	}
	second, err := readBoundedBundle(file)
	if err != nil || !bytes.Equal(first, second) {
		return nil, errors.New("source bundle file changed or exceeded its size limit")
	}
	return first, nil
}

func readBoundedBundle(file string) ([]byte, error) {
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBundleSize {
		return nil, errors.New("source bundle file is unavailable or invalid")
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, errors.New("source bundle file is unavailable or invalid")
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("source bundle file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(handle, maxBundleSize+1))
	if err != nil || len(raw) > maxBundleSize {
		return nil, errors.New("source bundle file exceeded its size limit")
	}
	after, err := os.Lstat(file)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("source bundle file changed while reading")
	}
	return raw, nil
}

func normalizeObservation(observation *SourceObservation) error {
	if !validSlug(observation.SourceID) || !sourceKinds[observation.Kind] || observation.Schema != sourceSchemas[observation.Kind] ||
		!safeOpaque(observation.AdapterVersion) || !safeOpaque(observation.Revision) || secretLike(observation.AdapterVersion) || secretLike(observation.Revision) {
		return errors.New("source observation identity is invalid")
	}
	switch observation.Quality {
	case QualityVerified, QualityInconclusive, QualityUnavailable, QualityUnsupported:
	default:
		return errors.New("source observation quality is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return errors.New("source observation time is invalid")
	}
	if observation.FreshnessPolicy != "" {
		if _, err := freshnessDuration(observation.FreshnessPolicy); err != nil {
			return err
		}
	}
	if observation.Payload == nil {
		return errors.New("source observation payload is required")
	}
	normalized, err := normalizeJSON(observation.Payload, "")
	if err != nil {
		return err
	}
	observation.Payload = normalized.(map[string]any)
	if err := validateObservationPayload(*observation); err != nil {
		return err
	}
	payloadBytes, err := canonicalJSON(observation.Payload)
	if err != nil || len(payloadBytes) > maxObservationSize {
		return errors.New("source observation exceeds its size limit")
	}
	if observation.Kind == "dagr" && containsForbiddenDagrIdentity(observation.Payload) {
		return errors.New("pre-start Dagr observation contains a future identity")
	}
	return nil
}

func validateObservationPayload(observation SourceObservation) error {
	if observation.Quality != QualityVerified {
		if err := validatePayloadFields(observation.Payload, []string{"status", "reasonCode"}, []string{"status"}); err != nil {
			return err
		}
		status, ok := observation.Payload["status"].(string)
		if !ok || status != string(observation.Quality) {
			return errors.New("source observation status does not match its quality")
		}
		return nil
	}
	var allowed, required []string
	switch observation.Schema {
	case "git.object/v1":
		allowed, required = []string{"repository", "objectId"}, []string{"repository", "objectId"}
	case "sergeant.td-observation/v1":
		allowed, required = []string{"resolutionNamespace", "sourceVersion", "evidenceDigest"}, []string{"resolutionNamespace", "sourceVersion", "evidenceDigest"}
	case "dagr.capability/v1":
		allowed, required = []string{"databaseIdentity", "schemaVersion", "operations"}, []string{"databaseIdentity", "schemaVersion", "operations"}
	case "sergeant.mission-source/v1":
		allowed, required = []string{"resolutionNamespace", "sourceVersion", "evidenceDigest"}, []string{"resolutionNamespace", "sourceVersion", "evidenceDigest"}
	case "platoon.receiving-capability/v1alpha1":
		allowed, required = []string{"capabilityRevision", "authorityRevision", "actionRevision", "environment", "production", "destructive", "decisionDigest"},
			[]string{"capabilityRevision", "authorityRevision", "actionRevision", "environment", "production", "destructive", "decisionDigest"}
	case "platoon.target-proof/v1alpha1":
		allowed, required = []string{"issuer", "trustRootRevision", "signature", "adapterIdentityDigest", "actionId", "targetId", "endpointDigest", "environment", "production", "destructive", "authorityRevision", "capabilityRevision", "issuedAt", "expiresAt", "proofDigest"},
			[]string{"issuer", "trustRootRevision", "signature", "adapterIdentityDigest", "actionId", "targetId", "endpointDigest", "environment", "production", "destructive", "authorityRevision", "capabilityRevision", "issuedAt", "expiresAt", "proofDigest"}
	case "platoon.validation-capability/v1alpha1":
		allowed, required = []string{"profileDigest", "executableDigest", "sandboxDigest", "policyDigest"}, []string{"profileDigest", "executableDigest", "sandboxDigest", "policyDigest"}
	case "platoon.policy/v1alpha1":
		allowed, required = []string{"policyDigest"}, []string{"policyDigest"}
	default:
		return errors.New("source observation schema is unsupported")
	}
	if err := validatePayloadFields(observation.Payload, allowed, required); err != nil {
		return err
	}
	stringFields := append([]string(nil), required...)
	boolFields := []string{}
	if observation.Schema == "dagr.capability/v1" {
		stringFields = []string{"databaseIdentity", "schemaVersion"}
	}
	if observation.Schema == "platoon.receiving-capability/v1alpha1" {
		stringFields = []string{"capabilityRevision", "authorityRevision", "environment", "decisionDigest"}
		stringFields = append(stringFields, "actionRevision")
		boolFields = []string{"production", "destructive"}
	}
	if observation.Schema == "platoon.target-proof/v1alpha1" {
		stringFields = []string{"issuer", "trustRootRevision", "signature", "adapterIdentityDigest", "actionId", "targetId", "endpointDigest", "environment", "authorityRevision", "capabilityRevision", "issuedAt", "expiresAt", "proofDigest"}
		boolFields = []string{"production", "destructive"}
	}
	for _, field := range stringFields {
		if _, ok := observation.Payload[field].(string); !ok {
			return errors.New("source observation field has the wrong type")
		}
	}
	for _, field := range boolFields {
		if _, ok := observation.Payload[field].(bool); !ok {
			return errors.New("source observation field has the wrong type")
		}
	}
	if observation.Schema == "git.object/v1" {
		objectID, _ := observation.Payload["objectId"].(string)
		if !fullObjectIDPattern.MatchString(objectID) {
			return errors.New("Git observation object identity is invalid")
		}
	}
	if observation.Schema == "dagr.capability/v1" {
		operations, ok := observation.Payload["operations"].([]any)
		if !ok || len(operations) == 0 {
			return errors.New("Dagr observation operations are invalid")
		}
		for _, operation := range operations {
			if value, ok := operation.(string); !ok || !safeOpaque(value) {
				return errors.New("Dagr observation operations are invalid")
			}
		}
		for _, required := range []string{"ack", "list", "load", "start", "watch"} {
			found := false
			for _, operation := range operations {
				found = found || operation == required
			}
			if !found {
				return errors.New("Dagr observation omits a required operation")
			}
		}
	}
	if observation.Schema == "platoon.receiving-capability/v1alpha1" || observation.Schema == "platoon.target-proof/v1alpha1" {
		environment, environmentOK := observation.Payload["environment"].(string)
		production, productionOK := observation.Payload["production"].(bool)
		destructive, destructiveOK := observation.Payload["destructive"].(bool)
		if !environmentOK || (environment != "development" && environment != "test" && environment != "staging") ||
			!productionOK || production || !destructiveOK || destructive {
			return errors.New("source observation target classification is invalid")
		}
	}
	if observation.Schema == "platoon.target-proof/v1alpha1" {
		issuedAt, issuedErr := time.Parse(time.RFC3339Nano, observation.Payload["issuedAt"].(string))
		expiresAt, expiresErr := time.Parse(time.RFC3339Nano, observation.Payload["expiresAt"].(string))
		if issuedErr != nil || expiresErr != nil || !expiresAt.After(issuedAt) {
			return errors.New("source observation target proof time is invalid")
		}
	}
	return nil
}

func validatePayloadFields(payload map[string]any, allowed, required []string) error {
	allowedSet := stringSet(allowed)
	for field := range payload {
		if !allowedSet[field] {
			return errors.New("source observation contains an unknown field")
		}
	}
	for _, field := range required {
		value, ok := payload[field]
		if !ok {
			return errors.New("source observation is missing a required field")
		}
		if text, isString := value.(string); isString && text == "" {
			return errors.New("source observation has an empty required field")
		}
	}
	return nil
}

func observationFresh(observation SourceObservation, evaluatedAt time.Time) error {
	observedAt, _ := time.Parse(time.RFC3339Nano, observation.ObservedAt)
	if observedAt.After(evaluatedAt.Add(time.Second)) {
		return errors.New("source observation is from the future")
	}
	if observation.FreshnessPolicy == "" {
		return nil
	}
	maximum, _ := freshnessDuration(observation.FreshnessPolicy)
	if evaluatedAt.Sub(observedAt) > maximum {
		return errors.New("source observation is stale")
	}
	return nil
}

func freshnessDuration(policy string) (time.Duration, error) {
	if !strings.HasPrefix(policy, "max-age:") {
		return 0, errors.New("source freshness policy is unsupported")
	}
	duration, err := time.ParseDuration(strings.TrimPrefix(policy, "max-age:"))
	if err != nil || duration < time.Second || duration > 30*24*time.Hour {
		return 0, errors.New("source freshness policy is invalid")
	}
	return duration, nil
}

func normalizeJSON(value any, field string) (any, error) {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			lower := strings.ToLower(key)
			if key == "" || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") ||
				strings.Contains(lower, "token") || lower == "raw" || strings.Contains(lower, "rawbody") || lower == "stdout" || lower == "stderr" ||
				strings.Contains(lower, "transcript") || strings.Contains(lower, "prompt") {
				return nil, errors.New("source observation contains a prohibited field")
			}
			normalized, err := normalizeJSON(child, key)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			normalized, err := normalizeJSON(child, field)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		if field == "operations" || field == "capabilities" || field == "schemas" {
			sort.Slice(result, func(i, j int) bool { return fmt.Sprint(result[i]) < fmt.Sprint(result[j]) })
		}
		return result, nil
	case []string:
		values := make([]any, len(current))
		for index, value := range current {
			values[index] = value
		}
		return normalizeJSON(values, field)
	case string:
		lower := strings.ToLower(current)
		if len(current) > maxObservationSize || hasControl(current) || strings.HasPrefix(current, "/") || strings.Contains(current, `\`) || secretLike(current) ||
			windowsAbsolutePath.MatchString(current) || strings.Contains(lower, "token=") || strings.Contains(lower, "password=") ||
			strings.Contains(lower, "secret=") || strings.Contains(lower, "authorization:") {
			return nil, errors.New("source observation contains a private or invalid value")
		}
		if strings.HasSuffix(strings.ToLower(field), "digest") && !digestPattern.MatchString(current) {
			return nil, errors.New("source observation digest is invalid")
		}
		return current, nil
	case bool, json.Number, float64:
		return current, nil
	case nil:
		return nil, errors.New("source observation contains null")
	default:
		return nil, errors.New("source observation contains an unsupported value")
	}
}

func secretLike(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "token=") || strings.Contains(lower, "password=") || strings.Contains(lower, "secret=") ||
		strings.Contains(lower, "authorization:") || secretValuePattern.MatchString(value)
}

func containsForbiddenDagrIdentity(payload map[string]any) bool {
	for key, value := range payload {
		normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
		if normalized == "workflowid" || normalized == "runid" || normalized == "stageid" || normalized == "stageids" {
			return true
		}
		if nested, ok := value.(map[string]any); ok && containsForbiddenDagrIdentity(nested) {
			return true
		}
	}
	return false
}

func canonicalDigest(domain string, value any) (string, error) {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	digest.Write([]byte(domain))
	digest.Write([]byte{0})
	digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(raw)
}

func parseStrictJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); err == nil || token != nil {
		return nil, errors.New("trailing JSON value")
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch current := token.(type) {
	case json.Delim:
		switch current {
		case '{':
			result := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("JSON object key is invalid")
				}
				if _, duplicate := result[key]; duplicate {
					return nil, errors.New("JSON object repeats a key")
				}
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				result[key] = child
			}
			_, err := decoder.Token()
			return result, err
		case '[':
			var result []any
			for decoder.More() {
				child, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				result = append(result, child)
			}
			_, err := decoder.Token()
			return result, err
		}
	case string, bool, json.Number:
		return current, nil
	case nil:
		return nil, nil
	}
	return nil, errors.New("JSON value is invalid")
}
