package manifest_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestLoadAppliesSafeDefaults(t *testing.T) {
	m, err := manifest.Load([]byte(validManifest))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if m.Spec.Limits.Implementation != 6 || m.Spec.Limits.Review != 2 {
		t.Fatalf("token defaults = %d/%d, want 6/2", m.Spec.Limits.Implementation, m.Spec.Limits.Review)
	}
	if m.Spec.Repositories[0].MaxWriters != 1 {
		t.Fatalf("MaxWriters = %d, want safe default 1", m.Spec.Repositories[0].MaxWriters)
	}
	if got := m.Spec.Limits.LeaseTTL; got != "5m" {
		t.Fatalf("LeaseTTL = %q, want 5m", got)
	}
}

func TestPublishedExampleAndSchema(t *testing.T) {
	if _, err := manifest.LoadFile("../../examples/platoon.yaml"); err != nil {
		t.Fatalf("published example is invalid: %v", err)
	}
	raw, err := os.ReadFile("../../schema/platoon.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if schema["$id"] != "https://platoon.example/schema/platoon-v1alpha1.json" {
		t.Fatalf("schema $id = %v", schema["$id"])
	}
}

func TestLoadRejectsUnsafeOrAmbiguousManifests(t *testing.T) {
	tests := map[string]struct {
		rewrite func(string) string
		want    string
	}{
		"unknown field": {
			rewrite: func(s string) string { return strings.Replace(s, "kind: Platoon", "kind: Platoon\nunknown: value", 1) },
			want:    "unknown",
		},
		"unsupported version": {
			rewrite: func(s string) string { return strings.Replace(s, "platoon.dev/v1alpha1", "platoon.dev/v2", 1) },
			want:    "apiVersion",
		},
		"explicit empty mission format": {
			rewrite: func(s string) string {
				return strings.Replace(s, "  mission: docs/mission.md", "  mission: docs/mission.md\n  missionFormat: \"\"", 1)
			},
			want: "missionFormat",
		},
		"cycle": {
			rewrite: func(s string) string { return strings.Replace(s, "dependsOn: []", "dependsOn: [verify]", 1) },
			want:    "cycle",
		},
		"claim traversal": {
			rewrite: func(s string) string { return strings.Replace(s, "internal/api", "../api", 1) },
			want:    "relative",
		},
		"absolute claim": {
			rewrite: func(s string) string { return strings.Replace(s, "internal/api", "/srv/api", 1) },
			want:    "relative",
		},
		"secret in command": {
			rewrite: func(s string) string {
				return strings.Replace(s, "args: [test, ./...]", "args: [test, token=not-allowed]", 1)
			},
			want: "secret",
		},
		"invalid branch prefix": {
			rewrite: func(s string) string { return strings.Replace(s, "feat/synthetic-work", "feat/bad~branch", 1) },
			want:    "branch",
		},
		"multiple documents": {
			rewrite: func(s string) string { return s + "\n---\nkind: Platoon\n" },
			want:    "document",
		},
		"scalar coercion": {
			rewrite: func(s string) string { return strings.Replace(s, "task: task-implement", "task: 123", 1) },
			want:    "string",
		},
		"duplicate task ownership": {
			rewrite: func(s string) string { return strings.Replace(s, "task: task-verify", "task: task-implement", 1) },
			want:    "task/repository",
		},
		"duplicate adopted fleet": {
			rewrite: func(s string) string {
				s = strings.Replace(s, "mode: implementation", "adoptFleet: existing-fleet\n      mode: implementation", 1)
				return strings.Replace(s, "mode: review", "adoptFleet: existing-fleet\n      mode: review", 1)
			},
			want: "adopted fleet",
		},
		"missing limits object": {
			rewrite: func(s string) string { return strings.Replace(s, "  limits: {}\n", "", 1) },
			want:    "limits is required",
		},
		"missing integration": {
			rewrite: func(s string) string {
				return strings.Replace(s, "      integration:\n        - executable: go\n          args: [test, ./...]\n", "", 1)
			},
			want: "integration is required",
		},
		"missing dependsOn": {
			rewrite: func(s string) string { return strings.Replace(s, "      dependsOn: []\n", "", 1) },
			want:    "dependsOn is required",
		},
		"missing claims": {
			rewrite: func(s string) string {
				return strings.Replace(s, "      claims:\n        paths: []\n        semantic: []\n", "", 1)
			},
			want: "claims is required",
		},
		"explicit zero limit": {
			rewrite: func(s string) string { return strings.Replace(s, "  limits: {}", "  limits: {implementation: 0}", 1) },
			want:    "limits.implementation",
		},
		"explicit zero writers": {
			rewrite: func(s string) string {
				return strings.Replace(s, "      branch: feat/synthetic-work", "      branch: feat/synthetic-work\n      maxWriters: 0", 1)
			},
			want: "maxWriters",
		},
		"glob claim": {
			rewrite: func(s string) string { return strings.Replace(s, "internal/api", "internal/*", 1) },
			want:    "literal",
		},
		"argument control": {
			rewrite: func(s string) string {
				return strings.Replace(s, "args: [test, ./...]", "args: [test, \"bad\\targ\"]", 1)
			},
			want: "control",
		},
		"argument escape": {
			rewrite: func(s string) string {
				return strings.Replace(s, "args: [test, ./...]", "args: [test, \"bad\\e[31m\"]", 1)
			},
			want: "control",
		},
		"argument backspace": {
			rewrite: func(s string) string {
				return strings.Replace(s, "args: [test, ./...]", "args: [test, \"bad\\barg\"]", 1)
			},
			want: "control",
		},
		"executable control": {
			rewrite: func(s string) string { return strings.Replace(s, "executable: go", "executable: \"go\\tbad\"", 1) },
			want:    "control",
		},
		"executable escape": {
			rewrite: func(s string) string { return strings.Replace(s, "executable: go", "executable: \"go\\ebad\"", 1) },
			want:    "control",
		},
		"executable backspace": {
			rewrite: func(s string) string { return strings.Replace(s, "executable: go", "executable: \"go\\bbad\"", 1) },
			want:    "control",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := manifest.Load([]byte(tc.rewrite(validManifest)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsYAMLAliases(t *testing.T) {
	raw := strings.Replace(validManifest,
		"metadata:\n  name: synthetic-platoon",
		"metadata: &metadata\n  name: synthetic-platoon",
		1,
	)
	raw = strings.Replace(raw, "spec:\n", "spec:\n  extraMetadata: *metadata\n", 1)
	_, err := manifest.Load([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("Load() error = %v, want alias rejection", err)
	}
}

const validManifest = `apiVersion: platoon.dev/v1alpha1
kind: Platoon
metadata:
  name: synthetic-platoon
spec:
  project: synthetic-project
  mission: docs/mission.md
  intent: docs/intent.md
  limits: {}
  adapters:
    dagr:
      executable: dagr
      database: .platoon/dagr.db
      inspectExecutable: sqlite3
    sergeant:
      fleetRoot: .platoon/fleets
      originProfile: platoon-local
      dispatch:
        executable: sgt-dispatch
      watch:
        executable: sgt-watch
      wake:
        executable: sgt-wake
      drain:
        executable: sgt-drain
  routing:
    - model: reasoning
      risk: high
      harness: opencode
  repositories:
    - id: synthetic-api
      path: repos/synthetic-api
      branch: feat/synthetic-work
      integration:
        - executable: go
          args: [test, ./...]
  stages:
    - id: implement
      repository: synthetic-api
      task: task-implement
      mode: implementation
      harness: opencode
      model: reasoning
      risk: high
      dependsOn: []
      claims:
        paths: [internal/api]
        semantic: [api-contract]
      acceptance:
        - executable: go
          args: [test, ./internal/api/...]
    - id: verify
      repository: synthetic-api
      task: task-verify
      mode: review
      harness: opencode
      model: reasoning
      risk: high
      dependsOn: [implement]
      claims:
        paths: []
        semantic: []
      acceptance:
        - executable: go
          args: [test, ./...]
`
