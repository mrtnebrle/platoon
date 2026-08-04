package opaqueid_test

import (
	"strings"
	"testing"

	"github.com/mrtnebrle/platoon/internal/opaqueid"
)

func TestValid(t *testing.T) {
	for _, value := range []string{"a", "task-1", "model_v1", "name.value", strings.Repeat("a", 128)} {
		if !opaqueid.Valid(value) {
			t.Errorf("Valid(%q) = false", value)
		}
	}
	for _, value := range []string{"", ".", "..", "a..b", "a/b", `a\b`, "a b", "é", strings.Repeat("a", 129)} {
		if opaqueid.Valid(value) {
			t.Errorf("Valid(%q) = true", value)
		}
	}
}
