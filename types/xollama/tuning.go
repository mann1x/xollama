package xollama

import (
	"fmt"
	"slices"
	"strings"
)

// Tuning carries opencoti flags a model can state, for the cases the engine's
// own defaults do not fit: each is unset unless a model or the server's
// defaults says otherwise, and each needs the opencoti engine.
type Tuning struct {
	// RecurrentStateType is --recurrent-state-type: bf16 halves the state of a
	// recurrent or hybrid model, and a prompt's logits then depend slightly on
	// the batch size; f32 matches stock llama.cpp at twice the memory.
	RecurrentStateType string `json:"recurrent_state_type,omitempty"`
	// FAQuantPrompt is --fa-quant-prompt: how a prompt reads a quantized KV on
	// CUDA. convert keeps an f16 ring, about 1 GB at 262144 cells.
	FAQuantPrompt string `json:"fa_quant_prompt,omitempty"`
	// AdmissionOnError is --polykv-adm-on-error: what the enforced gate does
	// when its own capacity check fails.
	AdmissionOnError string `json:"admission_on_error,omitempty"`
	// DraftKVarN is --spec-draft-kvarn: the drafter's KVarN width.
	DraftKVarN string `json:"draft_kvarn,omitempty"`
	// DraftTypeK and DraftTypeV are --cache-type-k-draft and -v-draft.
	DraftTypeK string `json:"draft_type_k,omitempty"`
	DraftTypeV string `json:"draft_type_v,omitempty"`
	// TailTypeK and TailTypeV are --cache-type-k-tail and -v-tail: the KV
	// spilled to the host.
	TailTypeK string `json:"tail_type_k,omitempty"`
	TailTypeV string `json:"tail_type_v,omitempty"`
	// StageWindow is --kvarn-stage-window, in 128-token groups; 0 is automatic.
	StageWindow int `json:"stage_window,omitempty"`
	// SlotInitialCtx is --slot-initial-ctx: cells a sequence starts with,
	// growing on demand. 0 allocates all of them.
	SlotInitialCtx int `json:"slot_initial_ctx,omitempty"`
	// SlotShrinkIdleMs is --slot-shrink-idle-ms: shrink a grown slot back after
	// this long idle. It needs SlotInitialCtx.
	SlotShrinkIdleMs int `json:"slot_shrink_idle_ms,omitempty"`
	// ElasticAllocTTL is --elastic-alloc-ttl: seconds an idle session keeps its
	// context booking. 0 is per request and -1 only an explicit close, so unset
	// is nil.
	ElasticAllocTTL *int `json:"elastic_alloc_ttl,omitempty"`
	// GPUShareWeight is --gpu-share-weight: the compute share against other
	// opencoti engines on one GPU. Zero is the engine's default.
	GPUShareWeight float64 `json:"gpu_share_weight,omitempty"`
}

// IsZero reports whether t states nothing.
func (t *Tuning) IsZero() bool { return t == nil || *t == (Tuning{}) }

var (
	validRecurrentStateTypes = []string{"bf16", "f32", "f16"}
	validFAQuantPrompts      = []string{"auto", "convert", "in-place"}
	validAdmissionOnError    = []string{"admit", "deny"}
	// There is no kvarn7: see the cache types in llm/engine_launch.go.
	validDraftKVarN = []string{"off", "inherit", "kvarn2", "kvarn3", "kvarn4", "kvarn5", "kvarn6", "kvarn8"}
)

// ValidRecurrentStateTypes returns the --recurrent-state-type values.
func ValidRecurrentStateTypes() []string { return slices.Clone(validRecurrentStateTypes) }

// ValidFAQuantPrompts returns the --fa-quant-prompt values.
func ValidFAQuantPrompts() []string { return slices.Clone(validFAQuantPrompts) }

// ValidAdmissionOnError returns the --polykv-adm-on-error values.
func ValidAdmissionOnError() []string { return slices.Clone(validAdmissionOnError) }

// ValidDraftKVarN returns the --spec-draft-kvarn values.
func ValidDraftKVarN() []string { return slices.Clone(validDraftKVarN) }

func (c *Config) validateTuning() error {
	t := c.Tuning
	if t.IsZero() {
		return nil
	}
	if c.Engine == EngineLlamaCpp {
		return fmt.Errorf("xollama config: tuning needs the opencoti engine; this config pins engine %q", c.Engine)
	}
	for _, s := range []struct {
		name, v string
		valid   []string
	}{
		{"recurrent_state_type", t.RecurrentStateType, validRecurrentStateTypes},
		{"fa_quant_prompt", t.FAQuantPrompt, validFAQuantPrompts},
		{"admission_on_error", t.AdmissionOnError, validAdmissionOnError},
		{"draft_kvarn", t.DraftKVarN, validDraftKVarN},
	} {
		if s.v != "" && !slices.Contains(s.valid, s.v) {
			return fmt.Errorf("xollama config: unknown tuning.%s %q (want one of %v)", s.name, s.v, s.valid)
		}
	}
	if (strings.HasPrefix(t.DraftTypeK, "kvarn") || strings.HasPrefix(t.DraftTypeV, "kvarn")) && t.DraftTypeK != t.DraftTypeV {
		return fmt.Errorf("xollama config: tuning.draft_type_k %q and draft_type_v %q: a KVarN width needs both, the same", t.DraftTypeK, t.DraftTypeV)
	}
	if t.StageWindow < 0 || t.SlotInitialCtx < 0 || t.SlotShrinkIdleMs < 0 || t.GPUShareWeight < 0 {
		return fmt.Errorf("xollama config: tuning: stage_window, slot_initial_ctx, slot_shrink_idle_ms and gpu_share_weight must not be negative")
	}
	if t.SlotShrinkIdleMs > 0 && t.SlotInitialCtx == 0 {
		return fmt.Errorf("xollama config: tuning.slot_shrink_idle_ms needs tuning.slot_initial_ctx: there is nothing to shrink back to")
	}
	if t.ElasticAllocTTL != nil && *t.ElasticAllocTTL < -1 {
		return fmt.Errorf("xollama config: tuning.elastic_alloc_ttl %d: want seconds, 0 or -1", *t.ElasticAllocTTL)
	}
	return nil
}
