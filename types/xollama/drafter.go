package xollama

import (
	"fmt"
	"regexp"
)

// DraftHeadNone is the draft.head value that detaches a model's drafter.
const DraftHeadNone = "none"

// MaxDraftTokens bounds draft.tokens. The engine recommends 2 to 3 and
// upstream's default is 4; past a few dozen a step proposes more than a
// verify pass ever accepts.
const MaxDraftTokens = 64

var draftHeadDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// IsZero reports whether the block states nothing.
func (d *Draft) IsZero() bool {
	return d == nil || (d.SpecType == "" && d.AutoMTPPolicy == "" && d.Head == "" && d.Tokens == nil)
}

// HeadDigest is the drafter draft.head names, or "" when it names none.
func (d *Draft) HeadDigest() string {
	if d == nil || d.Head == DraftHeadNone {
		return ""
	}
	return d.Head
}

func (d *Draft) validateHead() error {
	if d == nil {
		return nil
	}
	if d.Head != "" && d.Head != DraftHeadNone && !draftHeadDigest.MatchString(d.Head) {
		return fmt.Errorf("xollama config: draft.head %q is neither a sha256 digest nor %q", d.Head, DraftHeadNone)
	}
	if d.Tokens != nil && (*d.Tokens < 0 || *d.Tokens > MaxDraftTokens) {
		return fmt.Errorf("xollama config: draft.tokens %d is outside 0..%d", *d.Tokens, MaxDraftTokens)
	}
	return nil
}

// DrafterRef names the drafter published for a model: a Hugging Face
// repository and the file in it this fork attaches.
type DrafterRef struct {
	// Target is the model size the drafter was built for, as its publisher
	// names it ("26B-A4B").
	Target string
	Repo   string
	File   string
	// SpecType is the driver the drafter runs under.
	SpecType string
	// Tokens is the draft length measured best for it, set with the drafter
	// when the model states none.
	Tokens int
}

// Source is the reference `xollama tweak model --drafter` and the server's
// fetch both take.
func (r DrafterRef) Source() string { return "hf.co/" + r.Repo + "/" + r.File }

// gemma4Assistants are the Gemma 4 assistant heads, keyed by the target's
// embedding width.
//
// The width is what a head is built against: each one states it as
// gemma4-assistant.embedding_length_out, and a head whose value differs from
// the target's gemma4.embedding_length does not load. It is also what
// survives a fine-tune or a merge, where the name and general.size_label do
// not: a 98-expert merge of the 26B-A4B calls itself "98x2.6B" and is still
// 2816 wide. Read from the ten files on 2026-10-09
// (docs/features/gemma4-drafter.md).
var gemma4Assistants = map[uint64]string{
	1536: "E2B",
	2560: "E4B",
	2816: "26B-A4B",
	3840: "12B",
	5376: "31B",
}

// gemma4AssistantTokens is the draft length attached with a Gemma 4
// assistant. Upstream's default of 4 gains almost nothing with these heads:
// measured on c10 r3 with a 26B-A4B fine-tune at Q4_K_M and its Q8_0 head, RTX
// 3090, 256 tokens, three runs each -- no drafter 94.1 to 94.5 tok/s, length 4
// 93.6 to 96.3, length 3 101.9 to 103.4, length 2 110.5 to 112.6
// (/srv/ml/xc10/drafter/live2.out). opencoti's guide says 2 to 3.
const gemma4AssistantTokens = 2

// RecommendedDrafter returns the drafter published for a model of this
// architecture and embedding width, if there is one.
func RecommendedDrafter(arch string, embeddingLength uint64) (DrafterRef, bool) {
	if arch != "gemma4" {
		return DrafterRef{}, false
	}
	size, ok := gemma4Assistants[embeddingLength]
	if !ok {
		return DrafterRef{}, false
	}
	name := "gemma-4-" + size + "-it-assistant"
	return DrafterRef{
		Target:   size,
		Repo:     "ManniX-ITA/" + name + "-GGUF",
		File:     name + ".Q8_0.gguf",
		SpecType: "draft-assistant",
		Tokens:   gemma4AssistantTokens,
	}, true
}

// DrafterFits reports whether a drafter head can run against a target, and
// why not when it cannot. embeddingOut is the head's embedding_length_out; a
// drafter that states none is a model of its own and fits any target.
func DrafterFits(embeddingOut, targetEmbedding uint64) error {
	if embeddingOut == 0 || targetEmbedding == 0 || embeddingOut == targetEmbedding {
		return nil
	}
	return fmt.Errorf("the drafter is built for a model %d wide, and this model is %d wide", embeddingOut, targetEmbedding)
}
