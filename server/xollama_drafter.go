package server

import (
	"fmt"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// applyDraftTokens applies the model's draft.tokens under anything more
// specific. set is whether a PARAMETER or the request stated
// draft_num_predict; the result is whether anyone did, the model's config
// included.
//
// xollama-hook: drafter (called from modelOptionsWithEmbeddingBatchDefault)
func applyDraftTokens(m *Model, opts *api.Options, set bool) bool {
	if m == nil || set {
		return set
	}
	if n, ok := configDraftTokens(m); ok {
		opts.DraftNumPredict = n
		return true
	}
	return false
}

// configDraftTokens is the model's draft.tokens, or the server's default for
// it. It is the merge launchXollama does, without its log line: this runs
// for every request.
func configDraftTokens(m *Model) (int, bool) {
	cfg, _ := m.Xollama.WithDefaults(serverDefaults())
	if cfg == nil || cfg.Draft == nil || cfg.Draft.Tokens == nil {
		return 0, false
	}
	return *cfg.Draft.Tokens, true
}

// draftTurnedOff reports whether the model itself says not to draft: a
// PARAMETER draft_num_predict of 0, or with none, a draft.tokens of 0.
//
// It exists because a draft length of 0 means two things by the time a load
// reads it. A model with a head in its weights and nothing said also carries
// 0 (upstream zeroes the default where no drafter is attached), and opencoti
// drafts with that head on its own. "Off" has to reach the launch as a fact
// of its own for the engine to be told (llm.appendDraftOffArgs).
func draftTurnedOff(m *Model) bool {
	if m == nil {
		return false
	}
	if hasOption(m.Options, "draft_num_predict") {
		opts := api.DefaultOptions()
		_ = opts.FromMap(m.Options)
		return opts.DraftNumPredict <= 0
	}
	n, ok := configDraftTokens(m)
	return ok && n <= 0
}

// applyDraftHead makes the model's DRAFT layer what the request's draft.head
// says: a digest attaches that blob in place of any drafter the base has, and
// xollama.DraftHeadNone detaches it. A request that states no head, or no
// config at all, leaves the layers alone, so a Modelfile's DRAFT line and an
// inherited drafter work as upstream's.
//
// The blob must be in the store already: `xollama tweak model --drafter`
// uploads a local file or has the server fetch a Hugging Face one first. The
// head is checked against the base before it is attached, because one built
// for another size fails at load, where the reason is an engine log line.
//
// What is stored afterwards is the truth: the digest attached, and nothing
// for a detach, so the config never names a head the manifest does not carry.
//
// xollama-hook: drafter (called from CreateHandler)
func applyDraftHead(r *api.CreateRequest, layers []*modelLayer, config *model.ConfigV2, fn func(api.ProgressResponse)) ([]*modelLayer, error) {
	if r.Xollama == nil || r.Xollama.Draft == nil || r.Xollama.Draft.Head == "" {
		return layers, nil
	}
	head := r.Xollama.Draft.Head

	kept := layers[:0:0]
	var base *modelLayer
	for _, l := range layers {
		if l.MediaType == manifest.MediaTypeImageDraft {
			continue
		}
		if l.MediaType == "application/vnd.ollama.image.model" && base == nil {
			base = l
		}
		kept = append(kept, l)
	}
	config.Draft = nil

	if head == xollama.DraftHeadNone {
		r.Xollama.Draft.Head = ""
		if r.Xollama.Draft.IsZero() {
			r.Xollama.Draft = nil
		}
		return kept, nil
	}

	drafts, err := ggufLayersWithMediaType(head, "drafter.gguf", manifest.MediaTypeImageDraft, fn)
	if err != nil {
		return nil, fmt.Errorf("draft.head %s is not a GGUF in the model store; upload it first: %w", head, err)
	}
	for _, d := range drafts {
		if d.GGUF == nil || base == nil || base.GGUF == nil {
			continue
		}
		if want := d.GGUF.String("requires_target_arch"); want != "" && want != base.GGUF.Architecture() {
			return nil, fmt.Errorf("draft.head: the drafter requires a %q model, and this model is %q", want, base.GGUF.Architecture())
		}
		if err := xollama.DrafterFits(d.GGUF.Uint("embedding_length_out"), base.GGUF.Uint("embedding_length")); err != nil {
			return nil, fmt.Errorf("draft.head: %w", err)
		}
	}
	return append(kept, drafts...), nil
}
