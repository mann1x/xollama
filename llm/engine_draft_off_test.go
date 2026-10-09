package llm

import (
	"slices"
	"testing"
)

// Off is said to opencoti in its own words, and only when the model said
// off: one nobody configured keeps the engine's own choice, and stock
// llama.cpp gets upstream's command line either way.
func TestDraftOffIsSaidToOpencotiOnly(t *testing.T) {
	base := []string{"--model", "m.gguf"}
	for _, tc := range []struct {
		name      string
		draftType string
		off       bool
		opencoti  bool
		want      []string
	}{
		{"turned off, built-in head, opencoti", draftTypeMTP, true, true, []string{"--model", "m.gguf", "--spec-type", "none"}},
		{"turned off, attached drafter, opencoti", draftTypeAssistant, true, true, []string{"--model", "m.gguf", "--spec-type", "none"}},
		{"nobody said, built-in head, opencoti", draftTypeMTP, false, true, base},
		{"turned off, stock", draftTypeMTP, true, false, base},
		{"turned off, no drafter", "", true, true, base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := appendDraftOffArgs(slices.Clone(base), tc.draftType, tc.off, tc.opencoti)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}
