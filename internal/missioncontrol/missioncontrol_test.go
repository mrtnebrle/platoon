package missioncontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrtnebrle/platoon/internal/manifest"
)

func TestReadStableRejectsFileChangedDuringRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mission.yaml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readStableWithHook(path, func() {
		replacement := filepath.Join(dir, "replacement.yaml")
		if writeErr := os.WriteFile(replacement, []byte("changed"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if renameErr := os.Rename(replacement, path); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("readStableWithHook() error = %v", err)
	}
}

func TestValidateEffectsRejectsEveryMutatingReviewEffect(t *testing.T) {
	for _, effect := range []string{"write-claimed-source", "receiving-system-operation", "request-sergeant-lifecycle"} {
		t.Run(effect, func(t *testing.T) {
			m := &manifest.Manifest{Spec: manifest.Spec{Stages: []manifest.Stage{{ID: "review", Mode: manifest.Review}}}}
			value := &effects{
				Allowed: []string{effect}, Prohibited: []string{},
				Stages:  map[string][]string{"review": {effect}},
				Callers: map[string][]string{effect: {"operator"}},
			}
			class := "operate"
			if effect == "write-claimed-source" {
				class = "deliver"
			}
			if err := validateEffects(value, class, m); err == nil {
				t.Fatalf("validateEffects accepted %s for review stage", effect)
			}
		})
	}
}

func TestReadStableRejectsInPlaceChangeWithRestoredMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mission.yaml")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readStableWithHook(path, func() {
		if writeErr := os.WriteFile(path, []byte("modified"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if timeErr := os.Chtimes(path, info.ModTime(), info.ModTime()); timeErr != nil {
			t.Fatal(timeErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("readStableWithHook() error = %v", err)
	}
}

func TestDecodeRejectsAmbiguousYAMLFeatures(t *testing.T) {
	for name, raw := range map[string]string{
		"alias": "apiVersion: platoon.dev/mission/v1alpha1\nkind: Mission\nmetadata: &meta {name: sample}\nspec: *meta\n",
		"merge": "apiVersion: platoon.dev/mission/v1alpha1\nkind: Mission\nmetadata: &meta {name: sample}\nspec: {<<: *meta}\n",
		"null":  "apiVersion: platoon.dev/mission/v1alpha1\nkind: Mission\nmetadata: {name: null}\nspec: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decode([]byte(raw)); err == nil {
				t.Fatalf("decode accepted %s", name)
			}
		})
	}
}
