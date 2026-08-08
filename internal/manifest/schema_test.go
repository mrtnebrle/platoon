package manifest_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mrtnebrle/platoon/internal/manifest"
	"github.com/mrtnebrle/platoon/internal/opaqueid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestPublishedSchemaCompilesAndAcceptsExample(t *testing.T) {
	schema := compilePublishedSchema(t)
	document := loadExampleDocument(t)
	if err := schema.Validate(document); err != nil {
		t.Fatalf("published example fails schema: %v", err)
	}
	if _, err := manifest.LoadFile("../../examples/platoon.yaml"); err != nil {
		t.Fatalf("published example fails runtime: %v", err)
	}
}

func TestPublishedSchemasAcceptTypedExamples(t *testing.T) {
	manifestSchema := compilePublishedSchema(t)
	typedManifest := loadYAMLDocument(t, "../../examples/platoon-typed.yaml")
	if err := manifestSchema.Validate(typedManifest); err != nil {
		t.Fatalf("typed manifest fails schema: %v", err)
	}
	missionCompiler := jsonschema.NewCompiler()
	missionSchema, err := missionCompiler.Compile("../../schema/mission.schema.json")
	if err != nil {
		t.Fatalf("compile mission schema: %v", err)
	}
	mission := loadYAMLDocument(t, "../../examples/docs/mission-declaration.yaml")
	if err := missionSchema.Validate(mission); err != nil {
		t.Fatalf("typed mission fails schema: %v", err)
	}
}

func TestClaimPathSchemaRuntimeParity(t *testing.T) {
	schema := compilePublishedSchema(t)
	for _, value := range []string{
		"internal/api", "../api", "/absolute", "internal//api", "internal/api/", "internal/./api",
		`internal\api`, "internal/*", "internal/?", "internal/[ab]", "internal/\x1bapi",
	} {
		document := cloneDocument(t, loadExampleDocument(t))
		firstStage(document)["claims"].(map[string]any)["paths"] = []any{value}
		schemaValid := schema.Validate(document) == nil
		runtimeValid := manifest.ValidateClaimPath(value) == nil
		if schemaValid != runtimeValid {
			t.Errorf("claim %q parity: schema=%v runtime=%v", value, schemaValid, runtimeValid)
		}
	}
}

func TestOpaqueIDSchemaRuntimeParity(t *testing.T) {
	schema := compilePublishedSchema(t)
	for _, value := range []string{"valid-id", ".", "..", "a..b", "a/b", `a\b`, "a b", "é", strings.Repeat("a", 129)} {
		document := cloneDocument(t, loadExampleDocument(t))
		document["spec"].(map[string]any)["adapters"].(map[string]any)["sergeant"].(map[string]any)["originProfile"] = value
		schemaValid := schema.Validate(document) == nil
		if schemaValid != opaqueid.Valid(value) {
			t.Errorf("opaque ID %q parity: schema=%v runtime=%v", value, schemaValid, opaqueid.Valid(value))
		}
	}
}

func TestProjectAndRepositorySlugLengthParity(t *testing.T) {
	schema := compilePublishedSchema(t)
	tooLong := strings.Repeat("a", 129)
	for _, field := range []string{"project", "repository"} {
		document := cloneDocument(t, loadExampleDocument(t))
		if field == "project" {
			document["spec"].(map[string]any)["project"] = tooLong
		} else {
			document["spec"].(map[string]any)["repositories"].([]any)[0].(map[string]any)["id"] = tooLong
		}
		if err := schema.Validate(document); err == nil {
			t.Errorf("schema accepted overlong %s", field)
		}
	}
	if _, err := manifest.Load([]byte(strings.Replace(validManifest, "synthetic-project", tooLong, 1))); err == nil {
		t.Fatal("runtime accepted overlong project")
	}
	if _, err := manifest.Load([]byte(strings.Replace(validManifest, "synthetic-api", tooLong, 1))); err == nil {
		t.Fatal("runtime accepted overlong repository")
	}
}

func compilePublishedSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/platoon.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func loadExampleDocument(t *testing.T) map[string]any {
	t.Helper()
	return loadYAMLDocument(t, "../../examples/platoon.yaml")
}

func loadYAMLDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func cloneDocument(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func firstStage(document map[string]any) map[string]any {
	return document["spec"].(map[string]any)["stages"].([]any)[0].(map[string]any)
}
