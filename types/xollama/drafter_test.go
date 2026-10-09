package xollama

import (
	"strings"
	"testing"
)

// The five heads, by the width each was read to declare
// (gemma4-assistant.embedding_length_out) on 2026-10-09. A fine-tune keeps
// its base's width and so its drafter, whatever it calls itself.
func TestRecommendedDrafterGoesByWidth(t *testing.T) {
	for width, size := range map[uint64]string{1536: "E2B", 2560: "E4B", 2816: "26B-A4B", 3840: "12B", 5376: "31B"} {
		ref, ok := RecommendedDrafter("gemma4", width)
		if !ok {
			t.Fatalf("no drafter for a gemma4 %d wide", width)
		}
		want := "hf.co/ManniX-ITA/gemma-4-" + size + "-it-assistant-GGUF/gemma-4-" + size + "-it-assistant.Q8_0.gguf"
		if ref.Source() != want || ref.Target != size || ref.SpecType != "draft-assistant" {
			t.Fatalf("width %d: %+v (%s), want %s", width, ref, ref.Source(), want)
		}
	}
	if _, ok := RecommendedDrafter("gemma4", 4096); ok {
		t.Fatal("a width no head was built for got a drafter")
	}
	if _, ok := RecommendedDrafter("qwen35", 2816); ok {
		t.Fatal("another architecture got a Gemma 4 drafter")
	}
}

func TestDrafterFits(t *testing.T) {
	if err := DrafterFits(2816, 2816); err != nil {
		t.Fatal(err)
	}
	// A drafter that is a model of its own states no target width.
	if err := DrafterFits(0, 2816); err != nil {
		t.Fatal(err)
	}
	if err := DrafterFits(5376, 2816); err == nil || !strings.Contains(err.Error(), "5376") || !strings.Contains(err.Error(), "2816") {
		t.Fatalf("err = %v, want a refusal naming both widths", err)
	}
}

func TestDraftHeadAndTokensAreValidated(t *testing.T) {
	n := func(v int) *int { return &v }
	good := []*Draft{
		{Head: "sha256:" + strings.Repeat("a", 64)},
		{Head: DraftHeadNone},
		{Tokens: n(0)},
		{Tokens: n(MaxDraftTokens)},
	}
	for _, d := range good {
		if _, err := (&Config{Draft: d}).Marshal(); err != nil {
			t.Fatalf("%+v refused: %v", d, err)
		}
	}
	bad := []*Draft{
		{Head: "gemma-4-assistant.gguf"},
		{Head: "sha256:abc"},
		{Tokens: n(-1)},
		{Tokens: n(MaxDraftTokens + 1)},
	}
	for _, d := range bad {
		if _, err := (&Config{Draft: d}).Marshal(); err == nil {
			t.Fatalf("%+v accepted", d)
		}
	}
}

// draft.tokens changes how a model is served, so an older build must refuse
// it rather than draft a model whose publisher turned drafting off. The head
// is an upstream layer every build loads, and raises nothing.
func TestDraftTokensRaiseTheSchemaAndTheHeadDoesNot(t *testing.T) {
	zero := 0
	if got := (&Config{Draft: &Draft{Tokens: &zero}}).requiredVersion(); got != 8 {
		t.Fatalf("draft.tokens needs schema %d, want 8", got)
	}
	if got := (&Config{Draft: &Draft{Head: DraftHeadNone}}).requiredVersion(); got != SchemaVersionBase {
		t.Fatalf("draft.head needs schema %d, want the base", got)
	}
	if (&Config{Draft: &Draft{Tokens: &zero}}).IsZero() {
		t.Fatal("a config that turns drafting off was called empty")
	}
}

func TestADraftHeadIsNotAServerDefault(t *testing.T) {
	three := 3
	if err := (&Config{Draft: &Draft{Tokens: &three}}).ValidateDefaults(); err != nil {
		t.Fatalf("a default draft length was refused: %v", err)
	}
	if err := (&Config{Draft: &Draft{Head: "sha256:" + strings.Repeat("a", 64)}}).ValidateDefaults(); err == nil {
		t.Fatal("a drafter was accepted as a server default")
	}
}
