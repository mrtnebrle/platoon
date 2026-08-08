package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mrtnebrle/platoon/internal/opaqueid"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion      = "platoon.dev/v1alpha1"
	Kind            = "Platoon"
	maxManifestSize = 1 << 20

	Implementation Mode = "implementation"
	Review         Mode = "review"

	MissionReference           = "reference"
	MissionDeclarationV1Alpha1 = "declaration-v1alpha1"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Mode string

type Manifest struct {
	APIVersion string   `json:"apiVersion" yaml:"apiVersion"`
	Kind       string   `json:"kind" yaml:"kind"`
	Metadata   Metadata `json:"metadata" yaml:"metadata"`
	Spec       Spec     `json:"spec" yaml:"spec"`
}

type Metadata struct {
	Name string `json:"name" yaml:"name"`
}

type Spec struct {
	Project       string       `json:"project" yaml:"project"`
	Mission       string       `json:"mission" yaml:"mission"`
	MissionFormat string       `json:"missionFormat,omitempty" yaml:"missionFormat,omitempty"`
	Intent        string       `json:"intent" yaml:"intent"`
	Limits        Limits       `json:"limits" yaml:"limits"`
	Adapters      Adapters     `json:"adapters" yaml:"adapters"`
	Routing       []Route      `json:"routing" yaml:"routing"`
	Repositories  []Repository `json:"repositories" yaml:"repositories"`
	Stages        []Stage      `json:"stages" yaml:"stages"`
}

type Limits struct {
	Implementation int    `json:"implementation" yaml:"implementation"`
	Review         int    `json:"review" yaml:"review"`
	LeaseTTL       string `json:"leaseTTL" yaml:"leaseTTL"`
	CommandTimeout string `json:"commandTimeout" yaml:"commandTimeout"`
	MaxOutputBytes int    `json:"maxOutputBytes" yaml:"maxOutputBytes"`
}

type Adapters struct {
	Dagr     DagrAdapter     `json:"dagr" yaml:"dagr"`
	Sergeant SergeantAdapter `json:"sergeant" yaml:"sergeant"`
}

type DagrAdapter struct {
	Executable        string   `json:"executable" yaml:"executable"`
	Args              []string `json:"args" yaml:"args"`
	Database          string   `json:"database" yaml:"database"`
	InspectExecutable string   `json:"inspectExecutable" yaml:"inspectExecutable"`
}

type SergeantAdapter struct {
	FleetRoot     string  `json:"fleetRoot" yaml:"fleetRoot"`
	OriginProfile string  `json:"originProfile" yaml:"originProfile"`
	Dispatch      Command `json:"dispatch" yaml:"dispatch"`
	Watch         Command `json:"watch" yaml:"watch"`
	Wake          Command `json:"wake" yaml:"wake"`
	Drain         Command `json:"drain" yaml:"drain"`
}

type Command struct {
	Executable string   `json:"executable" yaml:"executable"`
	Args       []string `json:"args" yaml:"args"`
}

type Route struct {
	Model   string `json:"model" yaml:"model"`
	Risk    string `json:"risk" yaml:"risk"`
	Harness string `json:"harness" yaml:"harness"`
}

type Repository struct {
	ID          string    `json:"id" yaml:"id"`
	Path        string    `json:"path" yaml:"path"`
	Branch      string    `json:"branch" yaml:"branch"`
	MaxWriters  int       `json:"maxWriters" yaml:"maxWriters"`
	Integration []Command `json:"integration" yaml:"integration"`
}

type Stage struct {
	ID         string    `json:"id" yaml:"id"`
	Repository string    `json:"repository" yaml:"repository"`
	Task       string    `json:"task" yaml:"task"`
	Mode       Mode      `json:"mode" yaml:"mode"`
	Harness    string    `json:"harness" yaml:"harness"`
	Model      string    `json:"model" yaml:"model"`
	Risk       string    `json:"risk" yaml:"risk"`
	DependsOn  []string  `json:"dependsOn" yaml:"dependsOn"`
	Claims     Claims    `json:"claims" yaml:"claims"`
	Acceptance []Command `json:"acceptance" yaml:"acceptance"`
	AdoptFleet string    `json:"adoptFleet,omitempty" yaml:"adoptFleet,omitempty"`
}

type Claims struct {
	Paths    []string `json:"paths" yaml:"paths"`
	Semantic []string `json:"semantic" yaml:"semantic"`
}

func LoadFile(file string) (*Manifest, error) {
	result, _, err := LoadFileSnapshot(file)
	return result, err
}

func LoadFileSnapshot(file string) (*Manifest, []byte, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("manifest must be a regular file, not a symlink or device")
	}
	if info.Size() > maxManifestSize {
		return nil, nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestSize)
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest: %w", err)
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, nil, errors.New("manifest changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(handle, maxManifestSize+1))
	if err != nil || len(raw) > maxManifestSize {
		return nil, nil, errors.New("manifest could not be read within its size limit")
	}
	result, err := Load(raw)
	if err != nil {
		return nil, nil, err
	}
	return result, bytes.Clone(raw), nil
}

func Load(raw []byte) (*Manifest, error) {
	if len(raw) == 0 {
		return nil, errors.New("manifest is empty")
	}
	if len(raw) > maxManifestSize {
		return nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestSize)
	}

	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := rejectYAMLFeatures(&document); err != nil {
		return nil, err
	}
	if err := validateYAMLTypes(&document, ""); err != nil {
		return nil, err
	}
	if err := validateRequiredYAMLFields(&document, ""); err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var result Manifest
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing document: %w", err)
		}
		return nil, errors.New("multiple YAML documents are not allowed")
	}

	applyDefaults(&result, defaultFieldPresence(&document))
	if err := Validate(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

type fieldPresence struct {
	limits         map[string]bool
	repoMaxWriters []bool
}

func applyDefaults(m *Manifest, present fieldPresence) {
	if !present.limits["implementation"] {
		m.Spec.Limits.Implementation = 6
	}
	if !present.limits["review"] {
		m.Spec.Limits.Review = 2
	}
	if !present.limits["leaseTTL"] {
		m.Spec.Limits.LeaseTTL = "5m"
	}
	if !present.limits["commandTimeout"] {
		m.Spec.Limits.CommandTimeout = "2m"
	}
	if !present.limits["maxOutputBytes"] {
		m.Spec.Limits.MaxOutputBytes = 64 << 10
	}
	for i := range m.Spec.Repositories {
		if i >= len(present.repoMaxWriters) || !present.repoMaxWriters[i] {
			m.Spec.Repositories[i].MaxWriters = 1
		}
	}
}

func Validate(m *Manifest) error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if m.Kind != Kind {
		return fmt.Errorf("kind must be %q", Kind)
	}
	if err := validateSlug("metadata.name", m.Metadata.Name); err != nil {
		return err
	}
	if err := validateSlug("spec.project", m.Spec.Project); err != nil {
		return err
	}
	if err := validateReference("spec.mission", m.Spec.Mission); err != nil {
		return err
	}
	if m.Spec.MissionFormat != "" && m.Spec.MissionFormat != MissionReference && m.Spec.MissionFormat != MissionDeclarationV1Alpha1 {
		return errors.New("spec.missionFormat must be reference or declaration-v1alpha1")
	}
	if err := validateReference("spec.intent", m.Spec.Intent); err != nil {
		return err
	}
	if m.Spec.Limits.Implementation < 1 || m.Spec.Limits.Implementation > 128 {
		return errors.New("limits.implementation must be between 1 and 128")
	}
	if m.Spec.Limits.Review < 1 || m.Spec.Limits.Review > 128 {
		return errors.New("limits.review must be between 1 and 128")
	}
	if err := validateDuration("limits.leaseTTL", m.Spec.Limits.LeaseTTL); err != nil {
		return err
	}
	if err := validateDuration("limits.commandTimeout", m.Spec.Limits.CommandTimeout); err != nil {
		return err
	}
	if m.Spec.Limits.MaxOutputBytes < 1024 || m.Spec.Limits.MaxOutputBytes > 1<<20 {
		return errors.New("limits.maxOutputBytes must be between 1024 and 1048576")
	}
	if err := validateAdapters(m.Spec.Adapters); err != nil {
		return err
	}
	if len(m.Spec.Repositories) == 0 {
		return errors.New("at least one repository is required")
	}
	if len(m.Spec.Stages) == 0 {
		return errors.New("at least one stage is required")
	}

	repositories := make(map[string]Repository, len(m.Spec.Repositories))
	for i, repository := range m.Spec.Repositories {
		prefix := fmt.Sprintf("repositories[%d]", i)
		if err := validateSlug(prefix+".id", repository.ID); err != nil {
			return err
		}
		if _, exists := repositories[repository.ID]; exists {
			return fmt.Errorf("duplicate repository %q", repository.ID)
		}
		if strings.TrimSpace(repository.Path) == "" || hasControl(repository.Path) {
			return fmt.Errorf("%s.path is required", prefix)
		}
		if err := validateBranch(prefix+".branch", repository.Branch); err != nil {
			return err
		}
		if repository.MaxWriters < 1 || repository.MaxWriters > 128 {
			return fmt.Errorf("%s.maxWriters must be between 1 and 128", prefix)
		}
		for j, command := range repository.Integration {
			if err := validateCommand(fmt.Sprintf("%s.integration[%d]", prefix, j), command); err != nil {
				return err
			}
		}
		repositories[repository.ID] = repository
	}

	routes := make(map[string]Route, len(m.Spec.Routing))
	for i, route := range m.Spec.Routing {
		if !validOpaqueID(route.Model) || !validOpaqueID(route.Risk) || !validHarness(route.Harness) {
			return fmt.Errorf("routing[%d] requires model, risk, and harness", i)
		}
		key := route.Model + "\x00" + route.Risk
		if _, exists := routes[key]; exists {
			return fmt.Errorf("duplicate route for model %q and risk %q", route.Model, route.Risk)
		}
		routes[key] = route
	}
	if len(routes) == 0 {
		return errors.New("at least one model/risk route is required")
	}

	stages := make(map[string]Stage, len(m.Spec.Stages))
	stageOwnership := make(map[string]string, len(m.Spec.Stages))
	adoptedFleets := make(map[string]string, len(m.Spec.Stages))
	for i, stage := range m.Spec.Stages {
		prefix := fmt.Sprintf("stages[%d]", i)
		if err := validateSlug(prefix+".id", stage.ID); err != nil {
			return err
		}
		if len(stage.ID) > 24 {
			return fmt.Errorf("%s.id must be at most 24 characters", prefix)
		}
		if _, exists := stages[stage.ID]; exists {
			return fmt.Errorf("duplicate stage %q", stage.ID)
		}
		if _, exists := repositories[stage.Repository]; !exists {
			return fmt.Errorf("%s.repository %q is not declared", prefix, stage.Repository)
		}
		if !validOpaqueID(stage.Task) {
			return fmt.Errorf("%s.task must be a safe opaque identifier", prefix)
		}
		ownershipKey := stage.Repository + "\x00" + stage.Task
		if owner, exists := stageOwnership[ownershipKey]; exists {
			return fmt.Errorf("stages %q and %q reuse one task/repository ownership", owner, stage.ID)
		}
		stageOwnership[ownershipKey] = stage.ID
		if stage.Mode != Implementation && stage.Mode != Review {
			return fmt.Errorf("%s.mode must be implementation or review", prefix)
		}
		route, exists := routes[stage.Model+"\x00"+stage.Risk]
		if !exists || route.Harness != stage.Harness {
			return fmt.Errorf("%s model/risk/harness has no matching route", prefix)
		}
		if stage.AdoptFleet != "" && !validOpaqueID(stage.AdoptFleet) {
			return fmt.Errorf("%s.adoptFleet is invalid", prefix)
		}
		if stage.AdoptFleet != "" {
			if owner, exists := adoptedFleets[stage.AdoptFleet]; exists {
				return fmt.Errorf("stages %q and %q reuse one adopted fleet", owner, stage.ID)
			}
			adoptedFleets[stage.AdoptFleet] = stage.ID
		}
		repository := repositories[stage.Repository]
		if len(strings.TrimSuffix(repository.Branch, "-"))+1+len(stage.ID) > 240 {
			return fmt.Errorf("%s derived issue branch is too long", prefix)
		}
		if stage.Mode == Implementation && (len(stage.Claims.Paths) == 0 || len(stage.Claims.Semantic) == 0) {
			return fmt.Errorf("%s implementation requires path and semantic claims", prefix)
		}
		if stage.Mode == Review && (len(stage.Claims.Paths) != 0 || len(stage.Claims.Semantic) != 0) {
			return fmt.Errorf("%s review stages must be read-only with empty claims", prefix)
		}
		seenPaths := map[string]bool{}
		for j, claim := range stage.Claims.Paths {
			if err := ValidateClaimPath(claim); err != nil {
				return fmt.Errorf("%s.claims.paths[%d]: %w", prefix, j, err)
			}
			pathKey := strings.ToLower(claim)
			if seenPaths[pathKey] {
				return fmt.Errorf("%s has duplicate path claim %q", prefix, claim)
			}
			seenPaths[pathKey] = true
		}
		seenSemantic := map[string]bool{}
		for j, claim := range stage.Claims.Semantic {
			normalized, err := NormalizeSemantic(claim)
			if err != nil {
				return fmt.Errorf("%s.claims.semantic[%d]: %w", prefix, j, err)
			}
			if seenSemantic[normalized] {
				return fmt.Errorf("%s has duplicate semantic claim %q", prefix, normalized)
			}
			seenSemantic[normalized] = true
			stage.Claims.Semantic[j] = normalized
		}
		if len(stage.Acceptance) == 0 {
			return fmt.Errorf("%s requires at least one acceptance command", prefix)
		}
		for j, command := range stage.Acceptance {
			if err := validateCommand(fmt.Sprintf("%s.acceptance[%d]", prefix, j), command); err != nil {
				return err
			}
		}
		m.Spec.Stages[i] = stage
		stages[stage.ID] = stage
	}

	for _, stage := range m.Spec.Stages {
		seen := map[string]bool{}
		for _, dependency := range stage.DependsOn {
			if dependency == stage.ID {
				return fmt.Errorf("stage %q has a dependency cycle", stage.ID)
			}
			if _, exists := stages[dependency]; !exists {
				return fmt.Errorf("stage %q depends on missing stage %q", stage.ID, dependency)
			}
			if seen[dependency] {
				return fmt.Errorf("stage %q repeats dependency %q", stage.ID, dependency)
			}
			seen[dependency] = true
		}
	}
	if hasCycle(m.Spec.Stages) {
		return errors.New("stage dependency cycle detected")
	}
	return nil
}

func (l Limits) LeaseDuration() time.Duration {
	duration, _ := time.ParseDuration(l.LeaseTTL)
	return duration
}

func (l Limits) CommandDuration() time.Duration {
	duration, _ := time.ParseDuration(l.CommandTimeout)
	return duration
}

func (m *Manifest) Repository(id string) (Repository, bool) {
	for _, repository := range m.Spec.Repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return Repository{}, false
}

func (m *Manifest) Stage(id string) (Stage, bool) {
	for _, stage := range m.Spec.Stages {
		if stage.ID == id {
			return stage, true
		}
	}
	return Stage{}, false
}

func ResolveRuntimePaths(source *Manifest, sourceFile string) (*Manifest, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var result Manifest
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	manifestPath, err := filepath.Abs(sourceFile)
	if err != nil {
		return nil, err
	}
	base := filepath.Dir(manifestPath)
	result.Spec.Adapters.Dagr.Database = resolveRuntimePath(base, result.Spec.Adapters.Dagr.Database)
	result.Spec.Adapters.Dagr.Executable = resolveRuntimeExecutable(base, result.Spec.Adapters.Dagr.Executable)
	result.Spec.Adapters.Dagr.InspectExecutable = resolveRuntimeExecutable(base, result.Spec.Adapters.Dagr.InspectExecutable)
	result.Spec.Adapters.Sergeant.FleetRoot = resolveRuntimePath(base, result.Spec.Adapters.Sergeant.FleetRoot)
	result.Spec.Adapters.Sergeant.Dispatch.Executable = resolveRuntimeExecutable(base, result.Spec.Adapters.Sergeant.Dispatch.Executable)
	result.Spec.Adapters.Sergeant.Watch.Executable = resolveRuntimeExecutable(base, result.Spec.Adapters.Sergeant.Watch.Executable)
	result.Spec.Adapters.Sergeant.Wake.Executable = resolveRuntimeExecutable(base, result.Spec.Adapters.Sergeant.Wake.Executable)
	result.Spec.Adapters.Sergeant.Drain.Executable = resolveRuntimeExecutable(base, result.Spec.Adapters.Sergeant.Drain.Executable)
	for repositoryIndex := range result.Spec.Repositories {
		repository := &result.Spec.Repositories[repositoryIndex]
		repository.Path = resolveRuntimePath(base, repository.Path)
		for commandIndex := range repository.Integration {
			repository.Integration[commandIndex].Executable = resolveRuntimeExecutable(base, repository.Integration[commandIndex].Executable)
		}
	}
	for stageIndex := range result.Spec.Stages {
		for commandIndex := range result.Spec.Stages[stageIndex].Acceptance {
			command := &result.Spec.Stages[stageIndex].Acceptance[commandIndex]
			command.Executable = resolveRuntimeExecutable(base, command.Executable)
		}
	}
	return &result, nil
}

func resolveRuntimePath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, filepath.FromSlash(value))
}

func resolveRuntimeExecutable(base, value string) string {
	if filepath.IsAbs(value) || (!strings.Contains(value, "/") && !strings.Contains(value, `\`)) {
		return value
	}
	return resolveRuntimePath(base, value)
}

func ValidateClaimPath(value string) error {
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) || hasControl(value) {
		return errors.New("claim must be a non-empty relative slash path")
	}
	if strings.ContainsAny(value, "*?[]{}") {
		return errors.New("claim must be a literal path without glob metacharacters")
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("claim must be a normalized relative slash path without traversal")
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func NormalizeSemantic(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", "-", " ", "-", "/", "-").Replace(normalized)
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}
	if !slugPattern.MatchString(normalized) {
		return "", errors.New("semantic claim must use letters, digits, spaces, underscores, slashes, or hyphens")
	}
	return normalized, nil
}

func rejectYAMLFeatures(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not allowed")
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "<<" {
				return errors.New("YAML merge aliases are not allowed")
			}
		}
	}
	for _, child := range node.Content {
		if err := rejectYAMLFeatures(child); err != nil {
			return err
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
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("manifest mapping keys must be strings")
			}
			path := key.Value
			if field != "" {
				path = field + "." + key.Value
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
		if node.Tag == "!!null" {
			return fmt.Errorf("%s must not be null", field)
		}
		if integerYAMLField(field) {
			if node.Tag != "!!int" {
				return fmt.Errorf("%s must be an integer", field)
			}
		} else if node.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", field)
		}
		if field == "spec.stages[].adoptFleet" && node.Value == "" {
			return errors.New("spec.stages[].adoptFleet must not be empty when present")
		}
	}
	return nil
}

func validateRequiredYAMLFields(node *yaml.Node, field string) error {
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return errors.New("manifest must contain one document root")
		}
		return validateRequiredYAMLFields(node.Content[0], field)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := validateRequiredYAMLFields(child, field+"[]"); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make(map[string]bool, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		keys[key] = true
		childPath := key
		if field != "" {
			childPath = field + "." + key
		}
		if err := validateRequiredYAMLFields(node.Content[index+1], childPath); err != nil {
			return err
		}
	}
	for _, required := range requiredYAMLFields[field] {
		if !keys[required] {
			path := required
			if field != "" {
				path = field + "." + required
			}
			return fmt.Errorf("%s is required", path)
		}
	}
	return nil
}

var requiredYAMLFields = map[string][]string{
	"":                                  {"apiVersion", "kind", "metadata", "spec"},
	"metadata":                          {"name"},
	"spec":                              {"project", "mission", "intent", "limits", "adapters", "routing", "repositories", "stages"},
	"spec.adapters":                     {"dagr", "sergeant"},
	"spec.adapters.dagr":                {"executable", "database", "inspectExecutable"},
	"spec.adapters.sergeant":            {"fleetRoot", "originProfile", "dispatch", "watch", "wake", "drain"},
	"spec.adapters.sergeant.dispatch":   {"executable"},
	"spec.adapters.sergeant.watch":      {"executable"},
	"spec.adapters.sergeant.wake":       {"executable"},
	"spec.adapters.sergeant.drain":      {"executable"},
	"spec.routing[]":                    {"model", "risk", "harness"},
	"spec.repositories[]":               {"id", "path", "branch", "integration"},
	"spec.repositories[].integration[]": {"executable"},
	"spec.stages[]":                     {"id", "repository", "task", "mode", "harness", "model", "risk", "dependsOn", "claims", "acceptance"},
	"spec.stages[].claims":              {"paths", "semantic"},
	"spec.stages[].acceptance[]":        {"executable"},
}

func defaultFieldPresence(document *yaml.Node) fieldPresence {
	present := fieldPresence{limits: map[string]bool{}}
	root := document
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	spec := mappingValue(root, "spec")
	limits := mappingValue(spec, "limits")
	for _, field := range []string{"implementation", "review", "leaseTTL", "commandTimeout", "maxOutputBytes"} {
		present.limits[field] = mappingValue(limits, field) != nil
	}
	repositories := mappingValue(spec, "repositories")
	if repositories != nil && repositories.Kind == yaml.SequenceNode {
		present.repoMaxWriters = make([]bool, len(repositories.Content))
		for index, repository := range repositories.Content {
			present.repoMaxWriters[index] = mappingValue(repository, "maxWriters") != nil
		}
	}
	return present
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func integerYAMLField(field string) bool {
	return field == "spec.limits.implementation" || field == "spec.limits.review" ||
		field == "spec.limits.maxOutputBytes" || strings.HasSuffix(field, ".maxWriters")
}

func validateAdapters(adapters Adapters) error {
	if err := validateExecutable("adapters.dagr.executable", adapters.Dagr.Executable, adapters.Dagr.Args); err != nil {
		return err
	}
	if err := validateExecutable("adapters.dagr.inspectExecutable", adapters.Dagr.InspectExecutable, nil); err != nil {
		return err
	}
	if strings.TrimSpace(adapters.Dagr.Database) == "" || hasControl(adapters.Dagr.Database) {
		return errors.New("adapters.dagr.database is required")
	}
	if strings.TrimSpace(adapters.Sergeant.FleetRoot) == "" || hasControl(adapters.Sergeant.FleetRoot) {
		return errors.New("adapters.sergeant.fleetRoot is required")
	}
	if !validOpaqueID(adapters.Sergeant.OriginProfile) {
		return errors.New("adapters.sergeant.originProfile is required for crash-safe dispatch correlation")
	}
	for name, command := range map[string]Command{
		"dispatch": adapters.Sergeant.Dispatch,
		"watch":    adapters.Sergeant.Watch,
		"wake":     adapters.Sergeant.Wake,
		"drain":    adapters.Sergeant.Drain,
	} {
		if err := validateCommand("adapters.sergeant."+name, command); err != nil {
			return err
		}
	}
	return nil
}

func validateCommand(name string, command Command) error {
	return validateExecutable(name+".executable", command.Executable, command.Args)
}

func validateExecutable(name, executable string, args []string) error {
	if executable == "" || hasControl(executable) || strings.TrimSpace(executable) != executable {
		return fmt.Errorf("%s is required and must not contain control whitespace", name)
	}
	base := path.Base(executable)
	switch base {
	case "sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh":
		return fmt.Errorf("%s must not invoke a command shell", name)
	}
	for _, arg := range args {
		if hasControl(arg) {
			return fmt.Errorf("%s args must not contain control characters", name)
		}
		lower := strings.ToLower(arg)
		if strings.Contains(lower, "token=") || strings.Contains(lower, "password=") || strings.Contains(lower, "secret=") ||
			lower == "--token" || lower == "--password" || lower == "--secret" || strings.Contains(lower, "authorization:") {
			return fmt.Errorf("%s args must not contain a secret", name)
		}
	}
	return nil
}

func validateSlug(name, value string) error {
	if !slugPattern.MatchString(value) || !opaqueid.Valid(value) {
		return fmt.Errorf("%s must be a lowercase slug", name)
	}
	return nil
}

func validateReference(name, value string) error {
	if err := ValidateClaimPath(value); err != nil {
		return fmt.Errorf("%s must be a safe relative reference: %w", name, err)
	}
	return nil
}

func validateBranch(name, value string) error {
	if value == "" || len(value) > 200 || strings.ContainsAny(value, "\x00\r\n\t ~^:?*[\\") ||
		strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") ||
		strings.Contains(value, "//") || strings.Contains(value, "@{") {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func validateDuration(name, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil || duration < time.Second || duration > 24*time.Hour {
		return fmt.Errorf("%s must be a duration from 1s through 24h", name)
	}
	return nil
}

func validOpaqueID(value string) bool {
	return opaqueid.Valid(value)
}

func validHarness(value string) bool {
	return value == "opencode" || value == "goose" || value == "claude"
}

func hasCycle(stages []Stage) bool {
	dependencies := make(map[string][]string, len(stages))
	for _, stage := range stages {
		dependencies[stage.ID] = stage.DependsOn
	}
	const (
		unseen = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == visiting {
			return true
		}
		if state[id] == done {
			return false
		}
		state[id] = visiting
		for _, dependency := range dependencies[id] {
			if visit(dependency) {
				return true
			}
		}
		state[id] = done
		return false
	}
	for id := range dependencies {
		if visit(id) {
			return true
		}
	}
	return false
}
