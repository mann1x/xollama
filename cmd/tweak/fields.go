// Package tweak implements `xollama tweak`, which edits the fork's own model
// configuration -- the xollama.json layer described in
// docs/features/model-config.md -- without writing a Modelfile by hand.
//
// The settings in that layer are the reason this exists. They cannot be
// PARAMETERs (api.FormatParams rejects any name it does not know, so a model
// carrying one would fail to create on stock ollama), so the only way to set
// one was a Modelfile with a XOLLAMA directive holding hand-written JSON, which
// means knowing the field names, the value sets, and the half-dozen rules about
// which settings need which other settings. This command knows all three.
//
// Everything in this package reads ONE table, fields below. A setting is added
// by adding a row: the wizard, the flags, the review, the consistency pass and
// the help text all derive from it, so they cannot disagree about what exists.
package tweak

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// kind is how a field's value is asked for and parsed.
type kind int

const (
	// kindTri is a *bool: on, off, or not stated. The third state is not a
	// formality -- "this model says nothing" leaves the environment variable
	// in charge, and "this model says off" overrides it.
	kindTri kind = iota
	// kindChoice is a closed set plus "not stated".
	kindChoice
	// kindOpenChoice suggests a set but accepts anything, because the set a
	// build accepts depends on the engine serving the load.
	kindOpenChoice
	kindInt
	kindFloat
)

// field is one setting in the xollama.json layer.
type field struct {
	// name is the id everywhere: the flag (--dca), the prompt, the review row.
	name string
	// path is the JSON path it writes, for the review and for error messages
	// that have to match what someone would see in the file.
	path string
	// title is the one-line summary shown above the question.
	title string
	// help is the paragraph shown under it. It says what the setting does and
	// what happens if it is left alone, because the wizard's whole job is to
	// let someone answer without having read the schema.
	help string

	kind    kind
	choices func(*xollama.Config) []string
	// unit is shown with numeric prompts ("tokens", "MiB").
	unit string
	// env is the environment variable this setting falls through to when the
	// model states nothing. It is shown beside "not set", because "not set" is
	// an answer with a consequence and the operator should be able to see what
	// that consequence is without reading the docs. Empty where there is no
	// such variable: kv.unified is derived from other settings rather than
	// read from anywhere, and draft.spec_type is inferred from the drafter's
	// own metadata.
	env string

	// head marks a field whose bare flag scopes the wizard to its whole group
	// rather than to itself: `--dca` asks the DCA questions, `--dca=on` sets
	// this one field and asks nothing.
	head bool
	// group is the set a head expands to, in order.
	group []string
	// with names fields that must be asked alongside this one whatever the
	// scope, because they are only meaningful as a pair.
	with []string

	// get renders the stated value, or "" when the field is not stated.
	get func(*xollama.Config) string
	// set applies a value; "" clears the field.
	set func(*xollama.Config, string) error

	// blocked reports why this setting cannot be stated given the rest of the
	// config, or "" when it can. It covers only what makes a setting
	// MEANINGLESS or unservable on its own terms -- an engine that has no such
	// flag, a parent switch that is off. Cross-field rules that make a
	// combination wrong are left to xollama.Config.Validate, which is the
	// single authority for them; duplicating those here is how the two would
	// drift.
	blocked func(*xollama.Config) string
}

// tri renders a *bool for the review and the current-value line.
func tri(b *bool) string {
	switch {
	case b == nil:
		return ""
	case *b:
		return "on"
	default:
		return "off"
	}
}

// setTri parses on/off/yes/no/true/false/1/0, and treats the words that mean
// "let something else decide" as clearing the field. "auto" is one of them: on
// a tri-state there is no auto to state, there is only the absence of an
// opinion, which is what auto means to everything reading it.
func setTri(v string, dst **bool) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		*dst = nil
	case "on", "yes", "true", "1", "enable", "enabled":
		t := true
		*dst = &t
	case "off", "no", "false", "0", "disable", "disabled":
		f := false
		*dst = &f
	case "auto", "unset", "clear", "default", "none":
		*dst = nil
	default:
		return fmt.Errorf("want on, off or unset (got %q)", v)
	}
	return nil
}

// choice parses a value against a closed set, with the same clearing words.
func choice(v string, valid []string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "", "unset", "clear", "default", "none":
		return "", nil
	}
	for _, c := range valid {
		if s == c {
			return c, nil
		}
	}
	return "", fmt.Errorf("want one of %s (got %q)", strings.Join(valid, ", "), v)
}

// open parses a value that is only suggested, not constrained.
func open(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "unset", "clear", "default", "none":
		return ""
	}
	return s
}

func setInt(v string, dst *int) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "auto", "0":
		*dst = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("want a whole number or unset (got %q)", v)
	}
	if n < 0 {
		return fmt.Errorf("want a number that is not negative (got %q)", v)
	}
	*dst = n
	return nil
}

func setFloat(v string, dst *float64) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "auto", "0":
		*dst = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("want a number or unset (got %q)", v)
	}
	if f < 0 {
		return fmt.Errorf("want a number that is not negative (got %q)", v)
	}
	*dst = f
	return nil
}

func showInt(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func showFloat(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// The sub-structs are allocated on demand and pruned when they end up empty,
// so a config that states nothing marshals to nothing and clears the layer
// rather than leaving `"kv":{}` behind.
func kv(c *xollama.Config) *xollama.KV {
	if c.KV == nil {
		c.KV = &xollama.KV{}
	}
	return c.KV
}

func slots(c *xollama.Config) *xollama.Slots {
	if c.Slots == nil {
		c.Slots = &xollama.Slots{}
	}
	return c.Slots
}

func dca(c *xollama.Config) *xollama.DCA {
	if c.DCA == nil {
		c.DCA = &xollama.DCA{}
	}
	return c.DCA
}

func session(c *xollama.Config) *xollama.Session {
	if c.Session == nil {
		c.Session = &xollama.Session{}
	}
	return c.Session
}

func draft(c *xollama.Config) *xollama.Draft {
	if c.Draft == nil {
		c.Draft = &xollama.Draft{}
	}
	return c.Draft
}

// prune drops sub-structs that state nothing, so IsZero and the stored JSON
// agree with what the review printed.
func prune(c *xollama.Config) {
	if c.KV != nil && *c.KV == (xollama.KV{}) {
		c.KV = nil
	}
	if c.Slots != nil && c.Slots.Dynamic == nil && c.Slots.Max == 0 && c.Slots.TPSFloor == 0 &&
		c.Slots.VRAMReserveMiB == 0 && c.Slots.SWASeqBudget == 0 {
		c.Slots = nil
	}
	if c.DCA != nil && c.DCA.Enabled == nil && c.DCA.ChunkSize == 0 {
		c.DCA = nil
	}
	if c.Session != nil && c.Session.Affinity == nil && c.Session.Pool == nil && c.Session.MaxPools == 0 {
		c.Session = nil
	}
	if c.Draft != nil && c.Draft.SpecType == "" {
		c.Draft = nil
	}
}

// opencotiOnly is the reason a setting cannot be stated on a model that pins
// the stock engine. A model that states no engine is fine: the selector may
// still put opencoti under it, and refusing there would make every setting
// below unreachable without an engine pin.
func opencotiOnly(what string) func(*xollama.Config) string {
	return func(c *xollama.Config) string {
		if c.Engine == xollama.EngineLlamaCpp {
			return what + " needs the opencoti engine, and this model pins llamacpp"
		}
		return ""
	}
}

// offParent is the reason a setting cannot be stated while the switch it
// describes is off. Nil -- not stated -- is not off: the environment may still
// turn the feature on, and the number would then apply.
func offParent(name string, on func(*xollama.Config) *bool, what string) func(*xollama.Config) string {
	return func(c *xollama.Config) string {
		if b := on(c); b != nil && !*b {
			return what + ", and " + name + " is off"
		}
		return ""
	}
}

func dcaEnabled(c *xollama.Config) *bool {
	if c.DCA == nil {
		return nil
	}
	return c.DCA.Enabled
}

func slotsDynamic(c *xollama.Config) *bool {
	if c.Slots == nil {
		return nil
	}
	return c.Slots.Dynamic
}

func sessionPool(c *xollama.Config) *bool {
	if c.Session == nil {
		return nil
	}
	return c.Session.Pool
}

// cacheTypeChoices suggests the widths this build knows, narrowed by the
// engine the model pins. A model that pins nothing is offered both sets,
// because the selector decides at load time and either may be what runs.
func cacheTypeChoices(c *xollama.Config) []string {
	return llm.KnownCacheTypes(c.Engine != xollama.EngineLlamaCpp)
}

// fields is the table. Order is the order the wizard asks in, which is not
// arbitrary: engine comes first because it decides which later questions can be
// asked at all, and each group's switch comes before the numbers that only mean
// something once it is on.
var fields = []field{
	{
		name:  "engine",
		env:   "XOLLAMA_ENGINE",
		path:  "engine",
		title: "Engine — which inference engine this model needs",
		help: "Most models do not care, and leaving this unset is the normal answer: the\n" +
			"XOLLAMA_ENGINE selector then decides. Pin one only when the model genuinely\n" +
			"runs on one engine -- a gemma-4 drafter carrying masked_embd_* tensors is\n" +
			"rejected by stock llama.cpp's loader, so a model shipping one has to say so.\n" +
			"Pinning llamacpp also puts every opencoti-only setting below out of reach.",
		kind:    kindChoice,
		choices: func(*xollama.Config) []string { return []string{xollama.EngineOpencoti, xollama.EngineLlamaCpp} },
		get:     func(c *xollama.Config) string { return c.Engine },
		set: func(c *xollama.Config, v string) error {
			s, err := choice(v, []string{xollama.EngineOpencoti, xollama.EngineLlamaCpp})
			if err != nil {
				return err
			}
			c.Engine = s
			return nil
		},
	},
	{
		name:  "flash-attn",
		env:   "OLLAMA_FLASH_ATTENTION",
		path:  "flash_attention",
		title: "Flash attention — override the server's setting for this model",
		help: "The right answer is a property of the model and its cache, not of the machine:\n" +
			"some architectures give wrong answers with it, and some quantised cache types\n" +
			"need it. Unset leaves the server-wide OLLAMA_FLASH_ATTENTION in charge.",
		kind:    kindChoice,
		choices: func(*xollama.Config) []string { return []string{"on", "off", "auto"} },
		get:     func(c *xollama.Config) string { return c.FlashAttention },
		set: func(c *xollama.Config, v string) error {
			s, err := choice(v, []string{"on", "off", "auto"})
			if err != nil {
				return err
			}
			c.FlashAttention = s
			return nil
		},
	},
	{
		name:  "kv-k",
		env:   "XOLLAMA_K_CACHE_TYPE",
		path:  "kv.k",
		title: "KV cache type for keys",
		help: "Keys and values are separable because they are not equally sensitive:\n" +
			"quantising keys costs more quality than quantising values, so the usual recipe\n" +
			"is a wider type for K than for V. Upstream has one server-wide setting and\n" +
			"cannot express that. Unset falls through to the environment.\n" +
			"Anything may be typed here -- the list is what this build knows, not a limit.",
		kind:    kindOpenChoice,
		choices: cacheTypeChoices,
		with:    []string{"kv-v"},
		get:     func(c *xollama.Config) string { return orEmpty(c.KV != nil, func() string { return c.KV.K }) },
		set:     func(c *xollama.Config, v string) error { kv(c).K = open(v); return nil },
	},
	{
		name:    "kv-v",
		env:     "XOLLAMA_V_CACHE_TYPE",
		path:    "kv.v",
		title:   "KV cache type for values",
		help:    "The other half of the cache. Quantising values costs less quality than\nquantising keys, so this is the one to compress first.",
		kind:    kindOpenChoice,
		choices: cacheTypeChoices,
		with:    []string{"kv-k"},
		get:     func(c *xollama.Config) string { return orEmpty(c.KV != nil, func() string { return c.KV.V }) },
		set:     func(c *xollama.Config, v string) error { kv(c).V = open(v); return nil },
	},
	{
		name:  "kv-swa-k",
		env:   "XOLLAMA_K_CACHE_TYPE_SWA",
		path:  "kv.k_swa",
		title: "Ring cache type for keys (sliding-window models)",
		help: "Gemma-4 and its relatives keep a short-window ring alongside the global cache.\n" +
			"Compressing only the global half leaves most of the cost in place on those\n" +
			"models. The ring needs a KVarN base on both kv.k and kv.v, and both halves of\n" +
			"the ring must be set together -- the engine refuses anything else at startup.",
		kind:    kindOpenChoice,
		choices: cacheTypeChoices,
		with:    []string{"kv-swa-v"},
		blocked: opencotiOnly("a separate sliding-window ring cache"),
		get:     func(c *xollama.Config) string { return orEmpty(c.KV != nil, func() string { return c.KV.KSWA }) },
		set:     func(c *xollama.Config, v string) error { kv(c).KSWA = open(v); return nil },
	},
	{
		name:    "kv-swa-v",
		env:     "XOLLAMA_V_CACHE_TYPE_SWA",
		path:    "kv.v_swa",
		title:   "Ring cache type for values (sliding-window models)",
		help:    "The other half of the ring. Set with kv.k_swa or not at all.",
		kind:    kindOpenChoice,
		choices: cacheTypeChoices,
		with:    []string{"kv-swa-k"},
		blocked: opencotiOnly("a separate sliding-window ring cache"),
		get:     func(c *xollama.Config) string { return orEmpty(c.KV != nil, func() string { return c.KV.VSWA }) },
		set:     func(c *xollama.Config, v string) error { kv(c).VSWA = open(v); return nil },
	},
	{
		name:  "kv-unified",
		path:  "kv.unified",
		title: "Unified KV pool — one shared pool, or a fixed split per slot",
		help: "The total cell count is the same either way; what changes is who may use them.\n" +
			"A shared pool lets ONE long conversation use every cell, which is what a\n" +
			"single-user long-context model wants. A fixed split guarantees each slot its\n" +
			"share, which is what a model serving several short conversations wants.\n" +
			"Unset keeps the derivation: the server passes the shared pool exactly when it\n" +
			"has parked slots or a prefix pool to admit into. Saying off is refused while\n" +
			"dynamic slots or session pooling are on -- both are shares of these cells.",
		kind: kindTri,
		get: func(c *xollama.Config) string {
			return orEmpty(c.KV != nil, func() string { return tri(c.KV.Unified) })
		},
		set: func(c *xollama.Config, v string) error { return setTri(v, &kv(c).Unified) },
	},
	{
		name:  "kv-residency",
		path:  "kv.residency_mode",
		title: "Rolling-KV residency tactic — for a cache that does not fit in VRAM",
		help: "auto picks the position-window tactic when the cache is eligible and the head\n" +
			"split otherwise; head and window force one. The set is the engine's own, read\n" +
			"from its argument parser rather than from documentation.",
		kind: kindChoice,
		choices: func(*xollama.Config) []string {
			return []string{xollama.ResidencyAuto, xollama.ResidencyHead, xollama.ResidencyWindow}
		},
		blocked: opencotiOnly("the rolling-KV residency tactic"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.KV != nil, func() string { return c.KV.ResidencyMode })
		},
		set: func(c *xollama.Config, v string) error {
			s, err := choice(v, []string{xollama.ResidencyAuto, xollama.ResidencyHead, xollama.ResidencyWindow})
			if err != nil {
				return err
			}
			kv(c).ResidencyMode = s
			return nil
		},
	},
	{
		name:  "slots",
		env:   "XOLLAMA_DYNAMIC_SLOTS",
		path:  "slots.dynamic",
		title: "Dynamic slots — admit concurrency instead of reserving it",
		help: "Upstream reserves OLLAMA_NUM_PARALLEL slots' worth of KV when the model loads,\n" +
			"whether or not anyone uses them, and everything past that number queues. You\n" +
			"therefore have to guess: too low wastes the card, too high wastes the memory\n" +
			"the model itself needed. Dynamic slots remove the guess -- the cache becomes\n" +
			"one shared pool, slots are parked, and another is admitted only while there is\n" +
			"headroom. Unset leaves XOLLAMA_DYNAMIC_SLOTS in charge.",
		kind:  kindTri,
		head:  true,
		group: []string{"slots", "slots-max", "slots-tps-floor", "slots-vram-reserve", "slots-swa-budget"},
		get: func(c *xollama.Config) string {
			return orEmpty(c.Slots != nil, func() string { return tri(c.Slots.Dynamic) })
		},
		set: func(c *xollama.Config, v string) error { return setTri(v, &slots(c).Dynamic) },
	},
	{
		name:    "slots-max",
		env:     "XOLLAMA_MAX_PARALLEL",
		path:    "slots.max",
		title:   "Slot ceiling — the most concurrent requests this model may serve",
		help:    "Unset leaves the engine to admit on headroom alone.",
		kind:    kindInt,
		unit:    "requests",
		blocked: offParent("slots.dynamic", slotsDynamic, "a ceiling describes how slots are admitted"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.Slots != nil, func() string { return showInt(c.Slots.Max) })
		},
		set: func(c *xollama.Config, v string) error { return setInt(v, &slots(c).Max) },
	},
	{
		name:  "slots-tps-floor",
		env:   "XOLLAMA_SLOTS_TPS_FLOOR",
		path:  "slots.tps_floor",
		title: "Decode-rate floor — per-slot tokens/s to protect",
		help: "Another slot is not admitted if the projected per-slot rate would fall below\n" +
			"this. Unset admits on memory headroom alone.",
		kind:    kindFloat,
		unit:    "tokens/s",
		blocked: offParent("slots.dynamic", slotsDynamic, "a rate floor describes how slots are admitted"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.Slots != nil, func() string { return showFloat(c.Slots.TPSFloor) })
		},
		set: func(c *xollama.Config, v string) error { return setFloat(v, &slots(c).TPSFloor) },
	},
	{
		name:    "slots-vram-reserve",
		env:     "XOLLAMA_SLOTS_VRAM_RESERVE",
		path:    "slots.vram_reserve_mib",
		title:   "VRAM reserve — free memory that must remain before admitting a slot",
		help:    "Set this to stop a co-resident process being squeezed out. Unset reserves none.",
		kind:    kindInt,
		unit:    "MiB",
		blocked: offParent("slots.dynamic", slotsDynamic, "a memory reserve describes how slots are admitted"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.Slots != nil, func() string { return showInt(c.Slots.VRAMReserveMiB) })
		},
		set: func(c *xollama.Config, v string) error { return setInt(v, &slots(c).VRAMReserveMiB) },
	},
	{
		name:  "slots-swa-budget",
		env:   "XOLLAMA_SWA_SEQ_BUDGET",
		path:  "slots.swa_seq_budget",
		title: "Sliding-window sequence budget",
		help: "On a sliding-window model every sequence reserves a whole window up front,\n" +
			"used or not. This sizes the ring for that many sequences instead of for every\n" +
			"slot and pool the model could open. Unset is the safe answer. A budget below\n" +
			"the sequence count trades worst-case headroom for memory: a window is\n" +
			"reclaimable once its token leaves the sequence that owned it, so B windows\n" +
			"serve rather more than B short conversations -- but a sustained full-window\n" +
			"load against a smaller pool will run out. Worth setting where the sequence\n" +
			"count is large; at a ceiling of a few it saves little and risks something.",
		kind: kindInt,
		unit: "sequences",
		get: func(c *xollama.Config) string {
			return orEmpty(c.Slots != nil, func() string { return showInt(c.Slots.SWASeqBudget) })
		},
		set: func(c *xollama.Config, v string) error { return setInt(v, &slots(c).SWASeqBudget) },
	},
	{
		name:  "dca",
		env:   "XOLLAMA_DCA",
		path:  "dca.enabled",
		title: "DCA — serve this model past the context it was trained on",
		help: "Dual chunk attention routes the full-attention layers through chunked\n" +
			"positions so no query-key distance exceeds the window the model was trained\n" +
			"in. Without it, a request for more context than the model was trained on is\n" +
			"clamped back down, which is the right default. It needs an engine with the\n" +
			"chunked route and an architecture that has one; a load that asks for more\n" +
			"context than the model was trained on, on an architecture with no route, is\n" +
			"refused rather than served unprotected. Unset leaves XOLLAMA_DCA in charge.",
		kind:  kindTri,
		head:  true,
		group: []string{"dca", "dca-chunk"},
		get: func(c *xollama.Config) string {
			return orEmpty(c.DCA != nil, func() string { return tri(c.DCA.Enabled) })
		},
		set: func(c *xollama.Config, v string) error { return setTri(v, &dca(c).Enabled) },
	},
	{
		name:    "dca-chunk",
		env:     "XOLLAMA_DCA_CHUNK_SIZE",
		path:    "dca.chunk_size",
		title:   "DCA chunk length",
		help:    "Unset means auto, which is the model's own pretrain window and is almost\nalways the right answer.",
		kind:    kindInt,
		unit:    "tokens",
		blocked: offParent("dca.enabled", dcaEnabled, "a chunk length describes how the chunked route splits positions"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.DCA != nil, func() string { return showInt(c.DCA.ChunkSize) })
		},
		set: func(c *xollama.Config, v string) error { return setInt(v, &dca(c).ChunkSize) },
	},
	{
		name:  "session-affinity",
		env:   "XOLLAMA_SESSION_AFFINITY",
		path:  "session.affinity",
		title: "Session affinity — return a conversation to the slot holding its KV",
		help: "A model serving one long agent conversation wants this. A model answering\n" +
			"unrelated one-shot prompts does not, and pinning those to one slot would make\n" +
			"it worse. Unset leaves XOLLAMA_SESSION_AFFINITY in charge.",
		kind:  kindTri,
		head:  true,
		group: []string{"session-affinity", "session-pool", "session-max-pools"},
		get: func(c *xollama.Config) string {
			return orEmpty(c.Session != nil, func() string { return tri(c.Session.Affinity) })
		},
		set: func(c *xollama.Config, v string) error { return setTri(v, &session(c).Affinity) },
	},
	{
		name:  "session-pool",
		env:   "XOLLAMA_SESSION_POOL",
		path:  "session.pool",
		title: "Prefix pooling — share one copy of a common prefix between conversations",
		help: "A system prompt and its tool definitions are otherwise held once per\n" +
			"conversation. Pooling holds one physical copy. It needs session affinity: a\n" +
			"shared prefix has nothing to attach to without session identity.",
		kind: kindTri,
		get: func(c *xollama.Config) string {
			return orEmpty(c.Session != nil, func() string { return tri(c.Session.Pool) })
		},
		set: func(c *xollama.Config, v string) error { return setTri(v, &session(c).Pool) },
	},
	{
		name:  "session-max-pools",
		env:   "XOLLAMA_POLYKV_MAX_POOLS",
		path:  "session.max_pools",
		title: "How many distinct shared prefixes this model may hold at once",
		help: "Not free: each pool reserves a sequence id for the whole life of the runner,\n" +
			"and on a sliding-window model a reserved sequence costs its own window exactly\n" +
			"as a slot does. Size it by one prefix per distinct system prompt, not one per\n" +
			"conversation -- conversations sharing a prompt share the pool, which is the\n" +
			"point. Unset gets a small default.",
		kind:    kindInt,
		unit:    "pools",
		blocked: offParent("session.pool", sessionPool, "a count of prefixes sizes the pooling"),
		get: func(c *xollama.Config) string {
			return orEmpty(c.Session != nil, func() string { return showInt(c.Session.MaxPools) })
		},
		set: func(c *xollama.Config, v string) error { return setInt(v, &session(c).MaxPools) },
	},
	{
		name:  "spec-type",
		path:  "draft.spec_type",
		title: "Speculative decoding type for this model's drafter",
		help: "Overrides what would otherwise be inferred from the draft model's own\n" +
			"metadata. Unset means infer, which is what almost every model should do.\n" +
			"draft_num_predict is not here: it is an ordinary PARAMETER already.",
		kind:    kindChoice,
		choices: func(*xollama.Config) []string { return xollama.ValidSpecTypes() },
		get: func(c *xollama.Config) string {
			return orEmpty(c.Draft != nil, func() string { return c.Draft.SpecType })
		},
		set: func(c *xollama.Config, v string) error {
			s, err := choice(v, xollama.ValidSpecTypes())
			if err != nil {
				return err
			}
			draft(c).SpecType = s
			return nil
		},
	},
}

// orEmpty keeps every get func a one-liner without a nil check per field; a
// sub-struct that is absent states nothing, which is "".
func orEmpty(present bool, f func() string) string {
	if !present {
		return ""
	}
	return f()
}

func fieldByName(name string) (field, bool) {
	for _, f := range fields {
		if f.name == name {
			return f, true
		}
	}
	return field{}, false
}

// scopeOf is what a bare flag asks about. A head expands to its group, so
// `--dca` asks the DCA questions; anything else asks itself and whatever it is
// only meaningful beside.
func scopeOf(name string) []string {
	f, ok := fieldByName(name)
	if !ok {
		return nil
	}
	if f.head {
		return f.group
	}
	out := []string{name}
	for _, w := range f.with {
		if !contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// FallbackEnvVars returns the environment variables the settings fall through
// to, in table order and without repeats, so `xollama tweak --help` can list
// exactly the ones a model's own answers override.
//
// It takes the map rather than importing envconfig so that the names cannot be
// invented here: a variable this table names that envconfig does not declare is
// dropped, and one that envconfig renames stops being listed rather than being
// listed wrongly. TestEveryNamedFallbackIsARealEnvironmentVariable holds the
// other direction.
func FallbackEnvVars[V any](all map[string]V) []V {
	var out []V
	seen := map[string]bool{}
	for _, f := range fields {
		if f.env == "" || seen[f.env] {
			continue
		}
		seen[f.env] = true
		if v, ok := all[f.env]; ok {
			out = append(out, v)
		}
	}
	return out
}
