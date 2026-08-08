package missioncontrol

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"gopkg.in/yaml.v3"
)

const (
	declarationAPIVersion = "platoon.dev/mission/v1alpha1"
	declarationKind       = "Mission"
	maxDeclarationSize    = 1 << 20
)

var (
	slugPattern         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	schemaPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]+/v[0-9]+(?:alpha[0-9]+)?$`)
	fullObjectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type Preview struct {
	Mode        string                `json:"mode"`
	Schema      string                `json:"schema,omitempty"`
	Class       string                `json:"class,omitempty"`
	Ready       bool                  `json:"ready"`
	Outputs     []string              `json:"outputs,omitempty"`
	Sufficiency []SufficiencyDecision `json:"sufficiency,omitempty"`
}

type SufficiencyDecision struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type declaration struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   missionMetadata `yaml:"metadata"`
	Spec       missionSpec     `yaml:"spec"`
}

type missionMetadata struct {
	Name string `yaml:"name"`
}

type missionSpec struct {
	Objective            string                `yaml:"objective"`
	Class                string                `yaml:"class"`
	Effects              *effects              `yaml:"effects"`
	Stops                []stop                `yaml:"stops"`
	AuthorityAssumptions []authorityAssumption `yaml:"authorityAssumptions"`
	Unknowns             []unknown             `yaml:"unknowns"`
	Contradictions       []contradiction       `yaml:"contradictions"`
	Outputs              []output              `yaml:"outputs"`
	Handoffs             []handoff             `yaml:"handoffs"`
	Unattended           *unattended           `yaml:"unattended"`
	Sources              []source              `yaml:"sources"`
}

type effects struct {
	Allowed    []string            `yaml:"allowed"`
	Prohibited []string            `yaml:"prohibited"`
	Stages     map[string][]string `yaml:"stages"`
	Callers    map[string][]string `yaml:"callers"`
}

type stop struct {
	ID        string        `yaml:"id"`
	Predicate stopPredicate `yaml:"predicate"`
	Scope     stopScope     `yaml:"scope"`
	Route     string        `yaml:"route"`
}

type stopPredicate struct {
	Source   string `yaml:"source"`
	Field    string `yaml:"field"`
	Operator string `yaml:"operator"`
	Value    any    `yaml:"value,omitempty"`
}

type stopScope struct {
	Entry   bool     `yaml:"entry"`
	Stages  []string `yaml:"stages"`
	Effects []string `yaml:"effects"`
}

type authorityAssumption struct {
	ID               string   `yaml:"id"`
	Source           string   `yaml:"source"`
	Effects          []string `yaml:"effects"`
	Claim            string   `yaml:"claim"`
	RevisionPolicy   string   `yaml:"revisionPolicy"`
	ExpectedRevision string   `yaml:"expectedRevision,omitempty"`
	Route            string   `yaml:"route"`
	ActorRole        string   `yaml:"actorRole,omitempty"`
	Stage            string   `yaml:"stage,omitempty"`
}

type unknown struct {
	ID               string   `yaml:"id"`
	Question         string   `yaml:"question"`
	Blocking         bool     `yaml:"blocking"`
	AttemptedSources []string `yaml:"attemptedSources"`
	Route            string   `yaml:"route"`
}

type contradiction struct {
	ID                   string   `yaml:"id"`
	Sources              []string `yaml:"sources"`
	Decision             string   `yaml:"decision"`
	DispositionAuthority string   `yaml:"dispositionAuthority"`
}

type output struct {
	ID            string   `yaml:"id"`
	Category      string   `yaml:"category"`
	Stage         string   `yaml:"stage"`
	Schema        string   `yaml:"schema"`
	EvidenceRoles []string `yaml:"evidenceRoles"`
	GatesOutcome  string   `yaml:"gatesOutcome"`
}

type handoff struct {
	ID            string   `yaml:"id"`
	Producer      string   `yaml:"producer"`
	Consumer      string   `yaml:"consumer"`
	OutputSchema  string   `yaml:"outputSchema"`
	EvidenceRoles []string `yaml:"evidenceRoles"`
	OutputRole    string   `yaml:"outputRole"`
	Compatibility string   `yaml:"compatibility"`
	Freshness     string   `yaml:"freshness"`
	Missing       string   `yaml:"missing"`
	Incompatible  string   `yaml:"incompatible"`
}

type unattended struct {
	Requested bool `yaml:"requested"`
}

type source struct {
	ID                string `yaml:"id"`
	Kind              string `yaml:"kind"`
	Schema            string `yaml:"schema"`
	Locator           string `yaml:"locator"`
	Revision          string `yaml:"revision,omitempty"`
	ObservationPolicy string `yaml:"observationPolicy,omitempty"`
	Role              string `yaml:"role"`
	Reason            string `yaml:"reason"`
}

func Compile(m *manifest.Manifest, manifestFile string) (Preview, error) {
	mode := m.Spec.MissionFormat
	if mode == "" {
		mode = manifest.MissionReference
	}
	preview := Preview{Mode: mode, Ready: true}
	if mode == manifest.MissionReference {
		return preview, nil
	}

	declarationPath := filepath.Join(filepath.Dir(manifestFile), filepath.FromSlash(m.Spec.Mission))
	raw, err := readStable(declarationPath)
	if err != nil {
		return Preview{}, compileFailure(classifyReadError(err))
	}
	d, err := decode(raw)
	if err != nil {
		return Preview{}, compileFailure(classifyDecodeError(err))
	}
	if err := validate(d, m); err != nil {
		return Preview{}, compileFailure(classifyValidationError(err))
	}

	preview.Schema = d.APIVersion
	preview.Class = d.Spec.Class
	for _, output := range d.Spec.Outputs {
		preview.Outputs = append(preview.Outputs, output.Category)
	}
	sort.Strings(preview.Outputs)
	blocking := make([]string, 0, len(d.Spec.Unknowns)+len(d.Spec.Contradictions))
	for _, unknown := range d.Spec.Unknowns {
		if unknown.Blocking {
			blocking = append(blocking, "blocking unknown "+unknown.ID)
		}
	}
	for _, contradiction := range d.Spec.Contradictions {
		blocking = append(blocking, "unresolved contradiction "+contradiction.ID)
	}
	for _, stop := range d.Spec.Stops {
		if stop.Scope.Entry {
			blocking = append(blocking, "entry stop "+stop.ID+" cannot be disproved without source evidence")
		}
	}
	for _, source := range d.Spec.Sources {
		if source.ObservationPolicy != "" {
			blocking = append(blocking, "source "+source.ID+" requires an observation bundle")
		}
	}
	allowedEffects := stringSet(d.Spec.Effects.Allowed)
	for _, effect := range requiredEntryEffects {
		if !allowedEffects[effect] {
			blocking = append(blocking, "required effect "+effect+" is not allowed")
		}
	}
	if d.Spec.Unattended.Requested {
		blocking = append(blocking, "unattended qualification is unavailable in declaration preview mode")
	}
	sort.Strings(blocking)
	if len(blocking) == 0 {
		preview.Sufficiency = []SufficiencyDecision{{Status: "ready", Reason: "all required outputs are declared"}}
	} else {
		preview.Ready = false
		for _, reason := range blocking {
			preview.Sufficiency = append(preview.Sufficiency, SufficiencyDecision{Status: "not-ready", Reason: reason})
		}
	}
	return preview, nil
}

func readStable(file string) ([]byte, error) {
	return readStableWithHook(file, nil)
}

func readStableWithHook(file string, afterRead func()) ([]byte, error) {
	first, err := readBounded(file)
	if err != nil {
		return nil, err
	}
	if afterRead != nil {
		afterRead()
	}
	second, err := readBounded(file)
	if err != nil || !bytes.Equal(first, second) {
		return nil, errors.New("mission declaration changed while reading")
	}
	return first, nil
}

func readBounded(file string) ([]byte, error) {
	before, err := os.Lstat(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("mission declaration is missing")
		}
		return nil, errors.New("mission declaration cannot be inspected")
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("mission declaration must be a regular file, not a symlink or device")
	}
	if before.Size() > maxDeclarationSize {
		return nil, fmt.Errorf("mission declaration exceeds %d bytes", maxDeclarationSize)
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, errors.New("mission declaration cannot be opened")
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("mission declaration changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(handle, maxDeclarationSize+1))
	if err != nil || len(raw) > maxDeclarationSize {
		return nil, errors.New("mission declaration could not be read within its size limit")
	}
	after, err := os.Lstat(file)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("mission declaration changed while reading")
	}
	return bytes.Clone(raw), nil
}

func compileFailure(reason string) error {
	return fmt.Errorf("mission compile: mode=%s schema=%s reason=%s", manifest.MissionDeclarationV1Alpha1, declarationAPIVersion, reason)
}

func classifyReadError(err error) string {
	switch {
	case strings.Contains(err.Error(), "missing"):
		return "missing"
	case strings.Contains(err.Error(), "regular file"):
		return "not-regular"
	case strings.Contains(err.Error(), "exceeds") || strings.Contains(err.Error(), "size limit"):
		return "oversized"
	case strings.Contains(err.Error(), "changed"):
		return "changed"
	default:
		return "read-failed"
	}
}

func classifyDecodeError(err error) string {
	if strings.Contains(err.Error(), "field ") && strings.Contains(err.Error(), "not found") {
		return "unknown-field"
	}
	if strings.Contains(err.Error(), "mission stop") {
		return "malformed-stop"
	}
	if strings.Contains(err.Error(), "mission contradiction") {
		return "unrouted-contradiction"
	}
	if strings.Contains(err.Error(), "mission authority") {
		return "malformed-authority"
	}
	return "invalid-schema"
}

func classifyValidationError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "mission apiVersion") || strings.Contains(message, "mission kind must"):
		return "unknown-schema"
	case strings.Contains(message, "unknown effect"):
		return "unknown-effect"
	case strings.Contains(message, "unknown category"):
		return "unknown-output"
	case strings.Contains(message, "class ceiling"):
		return "effect-class-ceiling"
	case strings.Contains(message, "stop stage"):
		return "unknown-stop-stage"
	case strings.Contains(message, "stop"):
		return "malformed-stop"
	case strings.Contains(message, "authority"):
		return "malformed-authority"
	case strings.Contains(message, "contradiction"):
		return "unrouted-contradiction"
	case strings.Contains(message, "source"):
		return "invalid-source"
	case strings.Contains(message, "output"):
		return "invalid-output"
	case strings.Contains(message, "handoff"):
		return "invalid-handoff"
	default:
		return "invalid-declaration"
	}
}

func decode(raw []byte) (*declaration, error) {
	if len(raw) == 0 {
		return nil, errors.New("mission declaration is empty")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode mission declaration: %w", err)
	}
	if err := rejectYAMLFeatures(&document); err != nil {
		return nil, err
	}
	if err := validateRequiredFields(&document, ""); err != nil {
		return nil, err
	}
	if err := validateYAMLTypes(&document, ""); err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var result declaration
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode mission declaration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing mission document: %w", err)
		}
		return nil, errors.New("multiple mission YAML documents are not allowed")
	}
	return &result, nil
}

func rejectYAMLFeatures(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("mission YAML aliases are not allowed")
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return errors.New("mission YAML null values are not allowed")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("mission YAML mapping keys must be strings")
			}
			if key.Value == "<<" {
				return errors.New("mission YAML merge aliases are not allowed")
			}
			if seen[key.Value] {
				return fmt.Errorf("mission YAML repeats field %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := rejectYAMLFeatures(child); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredFields(node *yaml.Node, field string) error {
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return errors.New("mission declaration must contain one document root")
		}
		return validateRequiredFields(node.Content[0], field)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := validateRequiredFields(child, field+"[]"); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	present := map[string]bool{}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		present[key] = true
		childPath := key
		if field != "" {
			childPath = field + "." + key
		}
		if err := validateRequiredFields(node.Content[index+1], childPath); err != nil {
			return err
		}
	}
	for _, required := range requiredFields[field] {
		if !present[required] {
			switch {
			case strings.HasPrefix(field, "spec.stops[]"):
				return errors.New("mission stop is missing a required field")
			case field == "spec.contradictions[]":
				return errors.New("mission contradiction is missing a required field")
			case field == "spec.authorityAssumptions[]":
				return errors.New("mission authority assumption is missing a required field")
			}
			return errors.New("mission declaration is missing a required field")
		}
	}
	return nil
}

func validateYAMLTypes(node *yaml.Node, field string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateYAMLTypes(child, field); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			path := node.Content[index].Value
			if field != "" {
				path = field + "." + path
			}
			if err := validateYAMLTypes(node.Content[index+1], path); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLTypes(child, field+"[]"); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if booleanMissionField(field) {
			if node.Tag != "!!bool" {
				return errors.New("mission declaration boolean field has the wrong type")
			}
			return nil
		}
		if field == "spec.stops[].predicate.value" || field == "spec.stops[].predicate.value[]" {
			if node.Tag != "!!str" && node.Tag != "!!bool" {
				return errors.New("mission stop value has the wrong type")
			}
			return nil
		}
		if node.Tag != "!!str" {
			return errors.New("mission declaration string field has the wrong type")
		}
	}
	return nil
}

func booleanMissionField(field string) bool {
	return field == "spec.unattended.requested" || field == "spec.unknowns[].blocking" || field == "spec.stops[].scope.entry"
}

func validate(d *declaration, m *manifest.Manifest) error {
	if d.APIVersion != declarationAPIVersion {
		return fmt.Errorf("mission apiVersion must be %q", declarationAPIVersion)
	}
	if d.Kind != declarationKind {
		return fmt.Errorf("mission kind must be %q", declarationKind)
	}
	if !validSlug(d.Metadata.Name) {
		return errors.New("mission metadata.name must be a lowercase slug")
	}
	if strings.TrimSpace(d.Spec.Objective) == "" || len(d.Spec.Objective) > 4096 || hasControl(d.Spec.Objective) {
		return errors.New("mission objective is required and must be bounded text")
	}
	required, ok := requiredOutputs[d.Spec.Class]
	if !ok {
		return errors.New("mission class is unknown")
	}
	if d.Spec.Effects == nil || d.Spec.Effects.Allowed == nil || d.Spec.Effects.Prohibited == nil || d.Spec.Effects.Stages == nil || d.Spec.Effects.Callers == nil {
		return errors.New("mission effects must declare allowed, prohibited, stages, and callers")
	}
	if d.Spec.Stops == nil || d.Spec.AuthorityAssumptions == nil || d.Spec.Unknowns == nil || d.Spec.Contradictions == nil ||
		d.Spec.Outputs == nil || d.Spec.Handoffs == nil || d.Spec.Unattended == nil || d.Spec.Sources == nil {
		return errors.New("mission specification collections and unattended request are required")
	}
	if len(d.Spec.Stops) > 1024 || len(d.Spec.AuthorityAssumptions) > 1024 || len(d.Spec.Unknowns) > 1024 ||
		len(d.Spec.Contradictions) > 1024 || len(d.Spec.Outputs) > 1024 || len(d.Spec.Handoffs) > 1024 ||
		len(d.Spec.Sources) == 0 || len(d.Spec.Sources) > 1024 {
		return errors.New("mission declaration collection limit exceeded")
	}
	sourceIDs, sourceRoles, err := validateSources(d.Spec.Sources)
	if err != nil {
		return err
	}
	if err := validateEffects(d.Spec.Effects, d.Spec.Class, m); err != nil {
		return err
	}
	if err := validateStops(d.Spec.Stops, sourceIDs, sourceKindByID(d.Spec.Sources), d.Spec.Effects, m); err != nil {
		return err
	}
	if err := validateAuthority(d.Spec.AuthorityAssumptions, sourceIDs, sourceKindByID(d.Spec.Sources), sourceRevisionByID(d.Spec.Sources), d.Spec.Effects, m); err != nil {
		return err
	}
	if err := validateUnknowns(d.Spec.Unknowns, sourceIDs); err != nil {
		return err
	}
	if err := validateContradictions(d.Spec.Contradictions, sourceIDs); err != nil {
		return err
	}
	if err := validateDispositionOwners(d.Spec.Stops, d.Spec.Unknowns, d.Spec.Contradictions, d.Spec.AuthorityAssumptions); err != nil {
		return err
	}
	seenIDs := map[string]bool{}
	seenCategories := map[string]bool{}
	for _, output := range d.Spec.Outputs {
		if !validSlug(output.ID) || seenIDs[output.ID] {
			return errors.New("mission outputs require unique lowercase IDs")
		}
		seenIDs[output.ID] = true
		if _, ok := outputCategories[output.Category]; !ok {
			return fmt.Errorf("mission output %q has unknown category", output.ID)
		}
		if seenCategories[output.Category] {
			return fmt.Errorf("mission output category %q must be declared once", output.Category)
		}
		seenCategories[output.Category] = true
		if _, ok := m.Stage(output.Stage); !ok || !schemaPattern.MatchString(output.Schema) || len(output.EvidenceRoles) == 0 || output.GatesOutcome != "completed" {
			return fmt.Errorf("mission output %q has an invalid stage, schema, evidence roles, or outcome", output.ID)
		}
		if !allKnown(output.EvidenceRoles, sourceRoles) {
			return fmt.Errorf("mission output %q references an unknown evidence role", output.ID)
		}
		if hasDuplicates(output.EvidenceRoles) {
			return fmt.Errorf("mission output %q repeats an evidence role", output.ID)
		}
	}
	for _, category := range required {
		if !seenCategories[category] {
			return fmt.Errorf("mission class %q requires output category %q", d.Spec.Class, category)
		}
	}
	if err := validateHandoffs(d.Spec.Handoffs, m, sourceRoles); err != nil {
		return err
	}
	return nil
}

func validateSources(sources []source) (map[string]bool, map[string]bool, error) {
	ids := map[string]bool{}
	roles := map[string]bool{}
	for _, source := range sources {
		if !validSlug(source.ID) || ids[source.ID] {
			return nil, nil, errors.New("mission sources require unique lowercase IDs")
		}
		ids[source.ID] = true
		if _, ok := sourceKinds[source.Kind]; !ok {
			return nil, nil, fmt.Errorf("mission source %q has unknown kind", source.ID)
		}
		if source.Schema != sourceSchemas[source.Kind] {
			return nil, nil, errors.New("mission source schema does not match its kind")
		}
		if !safeOpaque(source.Locator) || !validSlug(source.Role) || roles[source.Role] || strings.TrimSpace(source.Reason) == "" || len(source.Reason) > 512 || hasControl(source.Reason) {
			return nil, nil, fmt.Errorf("mission source %q has an invalid locator, role, or reason", source.ID)
		}
		if (source.Revision == "") == (source.ObservationPolicy == "") ||
			(source.Revision != "" && !safeOpaque(source.Revision)) ||
			(source.ObservationPolicy != "" && !safeOpaque(source.ObservationPolicy)) {
			return nil, nil, fmt.Errorf("mission source %q requires exactly one safe revision or observationPolicy", source.ID)
		}
		if source.Kind == "git" && source.Revision != "" && !fullObjectIDPattern.MatchString(source.Revision) {
			return nil, nil, errors.New("mission git source revision must be a full object ID")
		}
		roles[source.Role] = true
	}
	return ids, roles, nil
}

func validateEffects(value *effects, class string, m *manifest.Manifest) error {
	allowed, err := closedSet("allowed", value.Allowed, effectRegistry)
	if err != nil {
		return err
	}
	prohibited, err := closedSet("prohibited", value.Prohibited, effectRegistry)
	if err != nil {
		return err
	}
	for effect := range allowed {
		if prohibited[effect] {
			return fmt.Errorf("mission effect %q cannot be allowed and prohibited", effect)
		}
		if globallyForbiddenEffects[effect] {
			return fmt.Errorf("mission effect %q is globally forbidden", effect)
		}
		if !commonEffects[effect] && !classEffectCeilings[class][effect] {
			return errors.New("mission effect exceeds class ceiling")
		}
	}
	for _, stage := range m.Spec.Stages {
		effects, exists := value.Stages[stage.ID]
		if !exists {
			return fmt.Errorf("mission effects.stages must include %q", stage.ID)
		}
		stageSet, err := closedSet("stage", effects, effectRegistry)
		if err != nil {
			return err
		}
		for effect := range stageSet {
			if !allowed[effect] {
				return fmt.Errorf("mission stage %q uses effect %q outside allowed", stage.ID, effect)
			}
			if stage.Mode == manifest.Review && effect == "write-claimed-source" {
				return errors.New("mission review stage cannot use a write effect")
			}
		}
	}
	for stage := range value.Stages {
		if _, ok := m.Stage(stage); !ok {
			return fmt.Errorf("mission effects.stages references unknown stage %q", stage)
		}
	}
	for effect := range allowed {
		callers, exists := value.Callers[effect]
		if !exists || len(callers) == 0 {
			return fmt.Errorf("mission allowed effect %q requires callers", effect)
		}
		if _, err := closedSet("caller", callers, callerRoles); err != nil {
			return err
		}
		effective := false
		for _, caller := range callers {
			if caller != "stage" || len(stagesForEffect(value.Stages, effect)) > 0 {
				effective = true
			}
		}
		if !effective {
			return errors.New("mission allowed effect has no effective caller tuple")
		}
	}
	for effect := range value.Callers {
		if !allowed[effect] {
			return fmt.Errorf("mission callers references effect %q outside allowed", effect)
		}
	}
	return nil
}

func validateStops(stops []stop, sources map[string]bool, sourceKindsByID map[string]string, effects *effects, m *manifest.Manifest) error {
	seen := map[string]bool{}
	allowed := stringSet(effects.Allowed)
	for _, stop := range stops {
		if !validSlug(stop.ID) || seen[stop.ID] || !sources[stop.Predicate.Source] ||
			!fieldPattern.MatchString(stop.Predicate.Field) || !stopOperators[stop.Predicate.Operator] ||
			!sources[stop.Route] || (!stop.Scope.Entry && len(stop.Scope.Stages) == 0 && len(stop.Scope.Effects) == 0) {
			return errors.New("mission stop is malformed")
		}
		seen[stop.ID] = true
		fieldType, knownField := stopFields[sourceKindsByID[stop.Predicate.Source]][stop.Predicate.Field]
		if !knownField {
			return errors.New("mission stop field is not in the source schema")
		}
		if stop.Predicate.Operator == "exists" && stop.Predicate.Value != nil {
			return fmt.Errorf("mission stop %q exists predicate must omit value", stop.ID)
		}
		if stop.Predicate.Operator != "exists" && stop.Predicate.Value == nil {
			return fmt.Errorf("mission stop %q predicate requires value", stop.ID)
		}
		if stop.Predicate.Operator == "quality_is" {
			if stop.Predicate.Field != "quality" {
				return errors.New("mission stop operator does not match the source field")
			}
			value, ok := stop.Predicate.Value.(string)
			if !ok || (value != "verified" && value != "inconclusive" && value != "unavailable") {
				return errors.New("mission stop quality predicate has an invalid value")
			}
		}
		if stop.Predicate.Field == "quality" && stop.Predicate.Operator != "quality_is" && stop.Predicate.Operator != "exists" {
			return errors.New("mission stop operator does not match the quality field")
		}
		if stop.Predicate.Operator == "in" {
			values, ok := stop.Predicate.Value.([]any)
			if !ok || len(values) == 0 || len(values) > 128 {
				return errors.New("mission stop in predicate requires a nonempty list")
			}
			for _, value := range values {
				if !validStopScalar(fieldType, value) {
					return errors.New("mission stop list value does not match the source field")
				}
			}
		}
		if (stop.Predicate.Operator == "equals" || stop.Predicate.Operator == "not_equals") && !validStopScalar(fieldType, stop.Predicate.Value) {
			return errors.New("mission stop value does not match the source field")
		}
		for _, effect := range stop.Scope.Effects {
			if !allowed[effect] {
				return fmt.Errorf("mission stop %q references effect outside allowed", stop.ID)
			}
		}
		if hasDuplicates(stop.Scope.Stages) || hasDuplicates(stop.Scope.Effects) {
			return fmt.Errorf("mission stop %q repeats scope values", stop.ID)
		}
		for _, stage := range stop.Scope.Stages {
			if _, ok := m.Stage(stage); !ok {
				return errors.New("mission stop stage is unknown")
			}
		}
	}
	return nil
}

func validStopScalar(fieldType string, value any) bool {
	switch fieldType {
	case "string":
		text, ok := value.(string)
		return ok && text != "" && len(text) <= 256 && !hasControl(text)
	case "bool":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func validateAuthority(assumptions []authorityAssumption, sources map[string]bool, sourceKindsByID, sourceRevisions map[string]string, effects *effects, m *manifest.Manifest) error {
	seen := map[string]bool{}
	for _, assumption := range assumptions {
		if !validSlug(assumption.ID) || seen[assumption.ID] || !sources[assumption.Source] || !authorityClaims[assumption.Claim] ||
			(assumption.RevisionPolicy != "exact" && assumption.RevisionPolicy != "current-at-invocation") || !sources[assumption.Route] {
			return errors.New("mission authority assumption is malformed")
		}
		seen[assumption.ID] = true
		if (assumption.RevisionPolicy == "exact") != (assumption.ExpectedRevision != "") ||
			(assumption.ExpectedRevision != "" && !safeOpaque(assumption.ExpectedRevision)) {
			return fmt.Errorf("mission authority assumption %q has malformed revision policy", assumption.ID)
		}
		if assumption.RevisionPolicy == "exact" && sourceRevisions[assumption.Source] != assumption.ExpectedRevision {
			return errors.New("mission authority exact revision does not match its source")
		}
		if (len(assumption.Effects) == 0 && assumption.Claim != "owner-may-disposition") || hasDuplicates(assumption.Effects) || !allKnown(assumption.Effects, stringSet(effects.Allowed)) {
			return fmt.Errorf("mission authority assumption %q has effects outside allowed", assumption.ID)
		}
		if assumption.Claim == "owner-may-disposition" && len(assumption.Effects) != 0 {
			return errors.New("mission authority disposition owner must not govern effects")
		}
		if assumption.Claim == "actor-may-attempt" {
			if !callerRoles[assumption.ActorRole] || (assumption.ActorRole == "stage") != (assumption.Stage != "") {
				return fmt.Errorf("mission authority assumption %q has malformed actor tuple", assumption.ID)
			}
			if assumption.Stage != "" {
				if _, ok := m.Stage(assumption.Stage); !ok {
					return fmt.Errorf("mission authority assumption %q references unknown stage", assumption.ID)
				}
			}
		} else if assumption.ActorRole != "" || assumption.Stage != "" {
			return fmt.Errorf("mission authority assumption %q has an unexpected actor tuple", assumption.ID)
		}
	}
	for effect, callers := range effects.Callers {
		for _, caller := range callers {
			stages := []string{""}
			if caller == "stage" {
				stages = stagesForEffect(effects.Stages, effect)
			}
			for _, stage := range stages {
				matches := 0
				for _, assumption := range assumptions {
					if assumption.Claim == "actor-may-attempt" && assumption.ActorRole == caller && assumption.Stage == stage && contains(assumption.Effects, effect) {
						matches++
					}
				}
				if matches != 1 {
					return fmt.Errorf("mission authority tuple for effect %q caller %q stage %q must match exactly one assumption", effect, caller, stage)
				}
			}
		}
	}
	for _, assumption := range assumptions {
		switch assumption.Claim {
		case "actor-may-attempt":
			for _, effect := range assumption.Effects {
				if !contains(effects.Callers[effect], assumption.ActorRole) ||
					(assumption.ActorRole == "stage" && !contains(effects.Stages[assumption.Stage], effect)) {
					return fmt.Errorf("mission authority assumption %q has an unmatched actor effect", assumption.ID)
				}
			}
		case "source-is-authoritative":
			for _, effect := range assumption.Effects {
				if !authorityKindAllows(effect, sourceKindsByID[assumption.Source]) {
					return errors.New("mission authority source kind does not own its effect")
				}
			}
		}
	}
	for _, effect := range effects.Allowed {
		matches := 0
		for _, assumption := range assumptions {
			if assumption.Claim == "source-is-authoritative" && contains(assumption.Effects, effect) && authorityKindAllows(effect, sourceKindsByID[assumption.Source]) {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("mission authority effect %q must match exactly one source-is-authoritative assumption", effect)
		}
	}
	return nil
}

func validateDispositionOwners(stops []stop, unknowns []unknown, contradictions []contradiction, assumptions []authorityAssumption) error {
	routes := map[string]bool{}
	for _, stop := range stops {
		routes[stop.Route] = true
	}
	for _, unknown := range unknowns {
		routes[unknown.Route] = true
	}
	for _, contradiction := range contradictions {
		routes[contradiction.DispositionAuthority] = true
	}
	for route := range routes {
		matches := 0
		for _, assumption := range assumptions {
			if assumption.Claim == "owner-may-disposition" && assumption.Source == route {
				matches++
			}
		}
		if matches != 1 {
			return errors.New("mission authority disposition route must match exactly one owner assumption")
		}
	}
	for _, assumption := range assumptions {
		if assumption.Claim == "owner-may-disposition" && !routes[assumption.Source] {
			return errors.New("mission authority owner assumption matches no disposition route")
		}
	}
	return nil
}

func validateUnknowns(unknowns []unknown, sources map[string]bool) error {
	seen := map[string]bool{}
	for _, unknown := range unknowns {
		if !validSlug(unknown.ID) || seen[unknown.ID] || strings.TrimSpace(unknown.Question) == "" ||
			len(unknown.Question) > 1024 || hasControl(unknown.Question) || len(unknown.AttemptedSources) == 0 ||
			hasDuplicates(unknown.AttemptedSources) || !allKnown(unknown.AttemptedSources, sources) || !sources[unknown.Route] {
			return errors.New("mission unknown is malformed")
		}
		seen[unknown.ID] = true
	}
	return nil
}

func validateContradictions(contradictions []contradiction, sources map[string]bool) error {
	seen := map[string]bool{}
	for _, contradiction := range contradictions {
		if !validSlug(contradiction.ID) || seen[contradiction.ID] || len(contradiction.Sources) < 2 ||
			hasDuplicates(contradiction.Sources) || !allKnown(contradiction.Sources, sources) || strings.TrimSpace(contradiction.Decision) == "" ||
			len(contradiction.Decision) > 512 || hasControl(contradiction.Decision) || !sources[contradiction.DispositionAuthority] {
			return errors.New("mission contradiction is malformed or unrouted")
		}
		seen[contradiction.ID] = true
	}
	return nil
}

func validateHandoffs(handoffs []handoff, m *manifest.Manifest, roles map[string]bool) error {
	seen := map[string]bool{}
	for _, handoff := range handoffs {
		_, producer := m.Stage(handoff.Producer)
		_, consumer := m.Stage(handoff.Consumer)
		if !validSlug(handoff.ID) || seen[handoff.ID] || !producer || !consumer || handoff.Producer == handoff.Consumer ||
			!schemaPattern.MatchString(handoff.OutputSchema) || len(handoff.EvidenceRoles) == 0 || !allKnown(handoff.EvidenceRoles, roles) ||
			hasDuplicates(handoff.EvidenceRoles) || !roles[handoff.OutputRole] || !safeOpaque(handoff.Compatibility) || !safeOpaque(handoff.Freshness) ||
			handoff.Missing != "block-consumer" || handoff.Incompatible != "reassemble" {
			return errors.New("mission handoff is malformed")
		}
		seen[handoff.ID] = true
	}
	return nil
}

func closedSet(name string, values []string, registry map[string]bool) (map[string]bool, error) {
	result := map[string]bool{}
	for _, value := range values {
		if !registry[value] {
			return nil, fmt.Errorf("mission %s has unknown effect or value %q", name, value)
		}
		if result[value] {
			return nil, fmt.Errorf("mission %s repeats %q", name, value)
		}
		result[value] = true
	}
	return result, nil
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func sourceKindByID(sources []source) map[string]string {
	result := make(map[string]string, len(sources))
	for _, source := range sources {
		result[source.ID] = source.Kind
	}
	return result
}

func sourceRevisionByID(sources []source) map[string]string {
	result := make(map[string]string, len(sources))
	for _, source := range sources {
		result[source.ID] = source.Revision
	}
	return result
}

func authorityKindAllows(effect, kind string) bool {
	allowed := effectAuthorityKinds[effect]
	return len(allowed) == 0 || allowed[kind]
}

func allKnown(values []string, known map[string]bool) bool {
	for _, value := range values {
		if !known[value] {
			return false
		}
	}
	return true
}

func stagesForEffect(stages map[string][]string, effect string) []string {
	result := []string{}
	for stage, effects := range stages {
		if contains(effects, effect) {
			result = append(result, stage)
		}
	}
	sort.Strings(result)
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func safeOpaque(value string) bool {
	if value == "" || len(value) > 256 || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._:-", r) {
			return false
		}
	}
	return true
}

func validSlug(value string) bool {
	return len(value) <= 128 && slugPattern.MatchString(value)
}

func hasDuplicates(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

var requiredOutputs = map[string][]string{
	"discover":         {"sourced-answers", "unknown-dispositions", "evidence-references", "finding-references"},
	"decide":           {"decision", "authority-decision-evidence", "decision-rationale", "downstream-effects"},
	"change-substrate": {"product-delta", "compatibility-evidence", "migration-evidence", "rollback-evidence", "dependency-evidence", "invalidation-evidence"},
	"deliver":          {"product-delta", "acceptance-evidence"},
	"validate":         {"independent-validation", "finding-references"},
	"operate":          {"operation-receipt", "resulting-state", "audit-evidence"},
	"recover":          {"safe-state", "cause-classification", "recovery-evidence", "recovery-disposition"},
	"learn":            {"world-delta", "finding-references", "capability-candidates", "workflow-candidates"},
}

var outputCategories = func() map[string]struct{} {
	result := map[string]struct{}{}
	for _, categories := range requiredOutputs {
		for _, category := range categories {
			result[category] = struct{}{}
		}
	}
	return result
}()

var fieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*$`)

var requiredFields = map[string][]string{
	"":                            {"apiVersion", "kind", "metadata", "spec"},
	"metadata":                    {"name"},
	"spec":                        {"objective", "class", "effects", "stops", "authorityAssumptions", "unknowns", "contradictions", "outputs", "handoffs", "unattended", "sources"},
	"spec.effects":                {"allowed", "prohibited", "stages", "callers"},
	"spec.stops[]":                {"id", "predicate", "scope", "route"},
	"spec.stops[].predicate":      {"source", "field", "operator"},
	"spec.stops[].scope":          {"entry", "stages", "effects"},
	"spec.authorityAssumptions[]": {"id", "source", "effects", "claim", "revisionPolicy", "route"},
	"spec.unknowns[]":             {"id", "question", "blocking", "attemptedSources", "route"},
	"spec.contradictions[]":       {"id", "sources", "decision", "dispositionAuthority"},
	"spec.outputs[]":              {"id", "category", "stage", "schema", "evidenceRoles", "gatesOutcome"},
	"spec.handoffs[]":             {"id", "producer", "consumer", "outputSchema", "evidenceRoles", "outputRole", "compatibility", "freshness", "missing", "incompatible"},
	"spec.unattended":             {"requested"},
	"spec.sources[]":              {"id", "kind", "schema", "locator", "role", "reason"},
}

var sourceKinds = map[string]bool{
	"git": true, "td": true, "dagr": true, "sergeant": true, "receiving-system": true,
	"environment-classifier": true, "validation-capability": true, "platoon-policy": true,
}

var sourceSchemas = map[string]string{
	"git": "git.object/v1", "td": "sergeant.td-observation/v1", "dagr": "dagr.capability/v1",
	"sergeant": "sergeant.mission-source/v1", "receiving-system": "platoon.receiving-capability/v1alpha1",
	"environment-classifier": "platoon.target-proof/v1alpha1", "validation-capability": "platoon.validation-capability/v1alpha1",
	"platoon-policy": "platoon.policy/v1alpha1",
}

var stopFields = map[string]map[string]string{
	"git":                    {"quality": "string", "revision": "string", "objectId": "string", "repository": "string"},
	"td":                     {"quality": "string", "revision": "string", "observedAt": "string"},
	"dagr":                   {"quality": "string", "schemaVersion": "string", "operations": "string"},
	"sergeant":               {"quality": "string", "revision": "string", "observedAt": "string"},
	"receiving-system":       {"quality": "string", "authorityRevision": "string", "environment": "string", "production": "bool", "destructive": "bool"},
	"environment-classifier": {"quality": "string", "environment": "string", "production": "bool", "destructive": "bool", "expiresAt": "string"},
	"validation-capability":  {"quality": "string", "profileDigest": "string"},
	"platoon-policy":         {"quality": "string", "revision": "string"},
}

var effectRegistry = map[string]bool{
	"read-source": true, "query-authority": true, "dagr-load-workflow": true, "dagr-start-run": true,
	"dagr-ack-stage": true, "sergeant-dispatch": true, "sergeant-coordinator-request": true,
	"publish-handoff": true, "route-finding": true, "run-validation": true, "write-claimed-source": true,
	"receiving-system-operation": true, "request-sergeant-lifecycle": true, "merge": true, "push": true,
	"identity-switch": true, "credential-creation": true, "production-activation": true,
	"child-state-write": true, "direct-session-injection": true,
}

var globallyForbiddenEffects = map[string]bool{
	"merge": true, "push": true, "identity-switch": true, "credential-creation": true,
	"production-activation": true, "child-state-write": true, "direct-session-injection": true,
}

var requiredEntryEffects = []string{
	"dagr-load-workflow", "dagr-start-run", "dagr-ack-stage", "sergeant-dispatch", "run-validation",
}

var commonEffects = map[string]bool{
	"read-source": true, "query-authority": true, "dagr-load-workflow": true, "dagr-start-run": true,
	"dagr-ack-stage": true, "sergeant-dispatch": true, "sergeant-coordinator-request": true,
	"publish-handoff": true, "route-finding": true, "run-validation": true,
}

var classEffectCeilings = map[string]map[string]bool{
	"discover":         {},
	"decide":           {},
	"change-substrate": {"write-claimed-source": true},
	"deliver":          {"write-claimed-source": true},
	"validate":         {},
	"operate":          {"receiving-system-operation": true, "request-sergeant-lifecycle": true},
	"recover":          {"write-claimed-source": true, "receiving-system-operation": true, "request-sergeant-lifecycle": true},
	"learn":            {},
}

var effectAuthorityKinds = map[string]map[string]bool{
	"dagr-load-workflow":           {"dagr": true},
	"dagr-start-run":               {"dagr": true},
	"dagr-ack-stage":               {"dagr": true},
	"sergeant-dispatch":            {"sergeant": true},
	"sergeant-coordinator-request": {"sergeant": true},
	"request-sergeant-lifecycle":   {"sergeant": true},
	"run-validation":               {"validation-capability": true},
	"write-claimed-source":         {"git": true},
	"receiving-system-operation":   {"receiving-system": true},
	"query-authority":              {"receiving-system": true, "environment-classifier": true},
	"publish-handoff":              {"git": true},
	"route-finding":                {"td": true, "sergeant": true},
}

var callerRoles = map[string]bool{"operator": true, "platoon": true, "external-scheduler": true, "stage": true}
var stopOperators = map[string]bool{"equals": true, "not_equals": true, "in": true, "exists": true, "quality_is": true}
var authorityClaims = map[string]bool{"source-is-authoritative": true, "actor-may-attempt": true, "owner-may-disposition": true}
