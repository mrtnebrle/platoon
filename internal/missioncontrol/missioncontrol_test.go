package missioncontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
