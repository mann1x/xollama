// Package xollama defines the fork's own model configuration: the settings a
// model needs that upstream ollama has no field for.
//
// WHY THIS IS NOT A PARAMETER. api.FormatParams resolves every Modelfile
// PARAMETER name against the json tags of api.Options and returns
// "unknown parameter '%s'" for anything it does not recognise. Adding fork
// fields there would make a model published from xollama fail to create on
// stock ollama — the opposite of what a soft fork should do.
//
// So the config travels as its own layer: media type
// application/vnd.ollama.image.json, named xollama.json. Upstream already
// reads that media type generically (manifest.ConfigLayer / ReadConfigJSON),
// its own json layers are named config.json and <prefix>/config.json, and the
// layer switch in server/images.go has no case for the media type at all.
// Stock ollama therefore pulls the blob, stores it and ignores it, while
// push and pull carry it unchanged because neither filters by media type.
package xollama

import (
	"encoding/json"
	"fmt"
	"slices"
)

// ConfigPath is the layer name the config is stored under. Upstream's own json
// layers are "config.json" and "<prefix>/config.json", so this cannot collide.
const ConfigPath = "xollama.json"

// MediaTypeImageJSON is the media type the config layer is stored as. It is
// upstream's, deliberately: a media type upstream already understands travels
// through every path that handles layers generically.
const MediaTypeImageJSON = "application/vnd.ollama.image.json"

// SchemaVersion is the newest schema this build can read. It is NOT
// necessarily what it writes: see requiredVersion.
const SchemaVersion = 2

// SchemaVersionBase is the version that expresses everything except the fields
// added in v2 (kv.unified, kv.residency_mode).
const SchemaVersionBase = 1

// Config is the contents of the xollama.json layer.
type Config struct {
	// Version is the schema version. Required.
	Version int `json:"version"`

	// Engine pins the inference engine this model needs, when it needs one:
	// "opencoti" or "llamacpp". Empty means the model does not care and the
	// XOLLAMA_ENGINE selector decides, which is the normal case.
	//
	// It exists because some models genuinely only run on one engine. A
	// gemma-4 E2B/E4B assistant drafter carries masked_embd_* tensors that
	// upstream llama.cpp's loader rejects, so a model shipping one has to say
	// so rather than fail at load with a vector range check.
	Engine string `json:"engine,omitempty"`

	// Draft carries drafter settings that have no api.Options equivalent.
	Draft *Draft `json:"draft,omitempty"`

	// KV carries this model's KV cache types. Upstream has one setting,
	// OLLAMA_KV_CACHE_TYPE, which is server-wide and writes the same type into
	// both halves of the cache -- so a model that wants a different answer, or
	// a different type for keys than for values, has nowhere to say it.
	KV *KV `json:"kv,omitempty"`

	// Slots carries this model's serving-capacity settings: how many requests
	// it may serve at once, and whether that number is fixed or grows with
	// demand.
	Slots *Slots `json:"slots,omitempty"`

	// DCA carries this model's dual chunk attention setting: whether it may be
	// served past the context it was trained on, and how long a chunk is.
	//
	// It belongs to the model rather than the server because it is a property
	// of the model, not a preference. Most models have no chunked attention
	// route at all, and the ones that do have their own pretrain window.
	DCA *DCA `json:"dca,omitempty"`

	// FlashAttention overrides the server's OLLAMA_FLASH_ATTENTION for this
	// model: "on", "off" or "auto". Empty means the model has no opinion.
	//
	// It belongs to the model because the right answer is a property of the
	// model and its cache, not of the machine: some architectures give wrong
	// answers with it, and some quantised cache types need it. A server-wide
	// switch forces one answer onto every model loaded.
	FlashAttention string `json:"flash_attention,omitempty"`

	// Session carries per-request engine session settings: which requests the
	// engine should treat as belonging to the same conversation, and whether
	// they may share a KV prefix pool with each other.
	//
	// It lives here rather than in an environment variable because the right
	// answer differs per model. A model serving one long agent conversation
	// wants affinity; a model answering unrelated one-shot prompts does not,
	// and pinning those to one slot would make it worse.
	Session *Session `json:"session,omitempty"`
}

// Slots holds this model's serving-capacity settings.
//
// Upstream reserves OLLAMA_NUM_PARALLEL slots' worth of KV when the model
// loads, whether or not anyone uses them, and everything past that number
// queues. You therefore have to guess: too low wastes the card, too high wastes
// the memory the model itself needed.
//
// Dynamic slots remove the guess. The cache becomes one shared pool instead of
// a fixed split, slots are allocated but parked, and the engine admits another
// only while there is headroom for it. Nothing is reserved for a slot nobody is
// using.
type Slots struct {
	// Dynamic turns the behaviour on or off for this model. Nil means the
	// model has no opinion and XOLLAMA_DYNAMIC_SLOTS decides.
	Dynamic *bool `json:"dynamic,omitempty"`

	// Max is the ceiling on concurrent requests. Zero means unstated.
	Max int `json:"max,omitempty"`

	// TPSFloor is the per-slot decode rate to protect: another slot is not
	// admitted if the projected rate would fall below it. Zero means unstated,
	// which leaves the engine to admit on memory headroom alone.
	TPSFloor float64 `json:"tps_floor,omitempty"`

	// VRAMReserveMiB is the free VRAM that must remain before another slot is
	// admitted, so a co-resident process is not squeezed out. Zero means
	// unstated.
	VRAMReserveMiB int `json:"vram_reserve_mib,omitempty"`

	// SWASeqBudget sizes the sliding-window cache for this many sequences
	// instead of for every slot and pool the model could open. It applies only
	// to sliding-window models, where each sequence otherwise reserves a whole
	// window up front whether or not anyone uses it.
	//
	// Zero means no budget, which reserves one window per sequence and is the
	// safe answer. A budget below that trades worst-case headroom for memory:
	// a window is reclaimable once its token leaves the sequence that owned it,
	// so B windows serve rather more than B short conversations -- but a
	// sustained full-window load against a smaller pool will run out.
	//
	// It is worth setting where the sequence count is large. At a ceiling of a
	// few it saves little and risks something.
	SWASeqBudget int `json:"swa_seq_budget,omitempty"`
}

// DCA holds this model's dual chunk attention setting.
//
// Enabling it lets the model be served past the context length its GGUF
// declares: the full-attention layers are routed through chunked positions so
// no query-key distance exceeds the window the model was trained in. Without
// it, a request for more context than the model was trained on is clamped back
// down, which is the right default.
//
// It needs an engine with the chunked route, and an architecture that has one.
// A load that asks for more context than the model was trained on, on an
// architecture with no route, is refused rather than served unprotected.
type DCA struct {
	// Enabled turns it on or off for this model. Nil means the model has no
	// opinion and XOLLAMA_DCA decides.
	Enabled *bool `json:"enabled,omitempty"`

	// ChunkSize is the chunk length in tokens. Zero means auto, which is the
	// model's own pretrain window and is almost always the right answer.
	ChunkSize int `json:"chunk_size,omitempty"`
}

// Session holds this model's session settings.
//
// Precedence, most specific first: the request, then this block, then the
// XOLLAMA_SESSION_AFFINITY environment variable, then the default. A pointer
// distinguishes "this model says off" from "this model says nothing", which an
// ordinary bool cannot.
type Session struct {
	// Affinity asks the engine to return a conversation to the slot that
	// already holds its KV, instead of choosing a slot by its own heuristics.
	// Nil means the model has no opinion.
	Affinity *bool `json:"affinity,omitempty"`

	// Pool asks for requests of this model to share one physical copy of their
	// common prefix -- a system prompt and tool definitions -- rather than one
	// copy per conversation. Nil means the model has no opinion.
	//
	// It is a request for the behaviour, not a pool identifier: pool ids are
	// handed out by the engine at runtime and cannot be known when a model is
	// published.
	Pool *bool `json:"pool,omitempty"`

	// MaxPools is how many distinct shared prefixes this model may hold at
	// once. Zero means unstated, and a model that asks for pooling without
	// naming a number gets a small default.
	//
	// It is not free: each pool reserves a sequence id for the whole life of
	// the runner, and on a sliding-window model a reserved sequence costs its
	// own window exactly as a slot does. One prefix per distinct system prompt
	// is the shape to size it by, not one per conversation -- conversations
	// sharing a prompt share the pool, which is the point.
	MaxPools int `json:"max_pools,omitempty"`
}

// KV holds this model's KV cache types.
//
// Keys and values are separable because they are not equally sensitive:
// quantising keys costs more quality than quantising values, so the usual
// recipe is a wider type for K than for V. Upstream cannot express that.
//
// Empty fields mean "not stated", and fall through to the environment and then
// to OLLAMA_KV_CACHE_TYPE. Types are not validated against a list here: the set
// a build accepts depends on which engine serves the load, and a config that
// refused a type this build has not heard of would make a model published by a
// newer xollama fail to create on an older one.
type KV struct {
	// K and V are the cache types for keys and values.
	K string `json:"k,omitempty"`
	V string `json:"v,omitempty"`

	// KSWA and VSWA are the types for the short-window half of a
	// sliding-window model -- the "ring" that Gemma-4 and its relatives keep
	// alongside the global cache. Compressing only the global half leaves most
	// of the cost in place on those models.
	//
	// They need an engine that has a separate ring cache; stock llama.cpp has
	// no such flag and a load that asks for one there is refused with a
	// message saying so, rather than started without it.
	KSWA string `json:"k_swa,omitempty"`
	VSWA string `json:"v_swa,omitempty"`

	// Unified says whether the cells are one pool shared across sequences
	// (--kv-unified) or a fixed per-slot split (--no-kv-unified). Schema v2.
	//
	// Until now this was DERIVED: the server passed --kv-unified exactly when
	// it had parked slots or a shared pool to admit into, and a model could
	// not say otherwise. It is stated here because the derivation answers a
	// different question from the one an operator sometimes has. A shared pool
	// lets ONE long conversation use every cell, which is what a single-user
	// long-context model wants; a fixed split guarantees each slot its share,
	// which is what a model serving several short conversations wants. The
	// total cell count is the same either way.
	//
	// nil means "not stated" and keeps the derivation. Stating false while
	// dynamic slots or shared pools are on is refused: parked slots have
	// nothing to be admitted into when every slot owns a fixed share, and a
	// pool's reserved sequence is a share of the same cells.
	Unified *bool `json:"unified,omitempty"`

	// ResidencyMode is the rolling-KV tactic for a cache that does not fit in
	// VRAM: "auto", "head" or "window". Schema v2.
	//
	// The set is the engine's own, read from its argument parser rather than
	// from a changelog: --kv-residency-mode must be auto|head|window. auto
	// picks the position-window tactic when the cache is eligible and the M2
	// head split otherwise; head and window force one.
	//
	// It is an opencoti extension -- stock llama.cpp has no such flag -- so a
	// model that pins engine "llamacpp" and also names a residency mode is
	// refused rather than served without it.
	ResidencyMode string `json:"residency_mode,omitempty"`
}

// Draft holds speculative-decoding settings for this model.
//
// draft_num_predict is deliberately NOT here: it is already an api.Options
// field and therefore already travels in the params layer. Only settings
// upstream has no home for belong in this struct.
type Draft struct {
	// SpecType overrides the --spec-type that would otherwise be inferred
	// from the draft model's own metadata. Empty means infer, which is what
	// almost every model should do.
	SpecType string `json:"spec_type,omitempty"`
}

// Engine values. These mirror the XOLLAMA_ENGINE selector, minus "auto":
// a model saying "auto" is the same as a model saying nothing.
const (
	EngineOpencoti = "opencoti"
	EngineLlamaCpp = "llamacpp"
)

var validEngines = []string{EngineOpencoti, EngineLlamaCpp}

// KV residency tactics. Taken from the engine's own parser -- it answers a bad
// value with "--kv-residency-mode must be auto|head|window" -- and not from
// documentation, so this list is a measurement of the artifact.
const (
	ResidencyAuto   = "auto"
	ResidencyHead   = "head"
	ResidencyWindow = "window"
)

var validResidencyModes = []string{ResidencyAuto, ResidencyHead, ResidencyWindow}

// validFlashAttention is what llama-server's --flash-attn accepts.
var validFlashAttention = []string{"on", "off", "auto"}

// Spec types a model may pin. Kept in step with llm/llama_server.go; the
// duplication is deliberate, because types must not import llm.
var validSpecTypes = []string{"draft-mtp", "draft-dflash", "draft-assistant", "draft-simple", "draft-eagle3", "draft-dspark"}

// Validate reports whether the config is one this build can act on.
//
// A version newer than this build's is an error rather than a warning. The
// fields here change how a model is served, so reading a v2 config as if it
// were v1 would run the model differently from how its publisher meant, and
// silently — which is the failure mode this whole layer exists to avoid.
func (c *Config) Validate() error {
	if c.Version <= 0 {
		return fmt.Errorf("xollama config: missing or invalid version %d", c.Version)
	}
	if c.Version > SchemaVersion {
		return fmt.Errorf("xollama config: schema version %d is newer than this build understands (%d); upgrade xollama", c.Version, SchemaVersion)
	}
	if c.Engine != "" && !slices.Contains(validEngines, c.Engine) {
		return fmt.Errorf("xollama config: unknown engine %q (want one of %v)", c.Engine, validEngines)
	}
	// Unlike a cache type, this IS a closed set: both engines take the same
	// three words and a fourth would be rejected by the engine's own parser
	// after the model had already been published.
	if c.FlashAttention != "" && !slices.Contains(validFlashAttention, c.FlashAttention) {
		return fmt.Errorf("xollama config: unknown flash_attention %q (want one of %v)", c.FlashAttention, validFlashAttention)
	}
	if c.Draft != nil && c.Draft.SpecType != "" && !slices.Contains(validSpecTypes, c.Draft.SpecType) {
		return fmt.Errorf("xollama config: unknown draft.spec_type %q (want one of %v)", c.Draft.SpecType, validSpecTypes)
	}
	// A pool is a shared prefix between requests the engine knows are
	// different conversations, so it has nothing to key on without affinity.
	// Refuse the combination at create time rather than serving a model whose
	// stated configuration cannot do what it says.
	if c.KV != nil {
		// The ring is one cache with two halves. Setting one type and leaving
		// the other to a different default is not a configuration anyone
		// means, and the engine refuses the pair at boot -- better to refuse
		// it while the model is being created, where the line is visible.
		if (c.KV.KSWA == "") != (c.KV.VSWA == "") {
			return fmt.Errorf("xollama config: kv.k_swa and kv.v_swa must be set together (got k_swa=%q, v_swa=%q)", c.KV.KSWA, c.KV.VSWA)
		}
		if c.KV.ResidencyMode != "" && !slices.Contains(validResidencyModes, c.KV.ResidencyMode) {
			return fmt.Errorf("xollama config: unknown kv.residency_mode %q (want one of %v)", c.KV.ResidencyMode, validResidencyModes)
		}
		// The rolling-KV residency tactic is an opencoti extension. A model
		// that pins the stock engine and also names one is asking for two
		// incompatible things; refuse it here, where the line is visible,
		// rather than serve it silently without the tactic it named.
		if c.KV.ResidencyMode != "" && c.Engine == EngineLlamaCpp {
			return fmt.Errorf("xollama config: kv.residency_mode needs the opencoti engine; this config pins engine %q", c.Engine)
		}
		// A fixed per-slot split has nothing for a parked slot to be admitted
		// into, and a shared prefix pool's reserved sequence is a share of the
		// same cells. Both features require the unified pool, so saying no to
		// it while asking for either is a configuration that cannot be served.
		if c.KV.Unified != nil && !*c.KV.Unified {
			if c.Slots != nil && c.Slots.Dynamic != nil && *c.Slots.Dynamic {
				return fmt.Errorf("xollama config: slots.dynamic needs kv.unified; a parked slot has nothing to be admitted into when every slot owns a fixed share of the cells")
			}
			if c.Session != nil && c.Session.Pool != nil && *c.Session.Pool {
				return fmt.Errorf("xollama config: session.pool needs kv.unified; a shared prefix pool is a share of the same cells")
			}
		}
	}
	if c.Slots != nil {
		if c.Slots.Max < 0 {
			return fmt.Errorf("xollama config: slots.max %d must not be negative", c.Slots.Max)
		}
		if c.Slots.TPSFloor < 0 {
			return fmt.Errorf("xollama config: slots.tps_floor %v must not be negative", c.Slots.TPSFloor)
		}
		if c.Slots.VRAMReserveMiB < 0 {
			return fmt.Errorf("xollama config: slots.vram_reserve_mib %d must not be negative", c.Slots.VRAMReserveMiB)
		}
		if c.Slots.SWASeqBudget < 0 {
			return fmt.Errorf("xollama config: slots.swa_seq_budget %d must not be negative", c.Slots.SWASeqBudget)
		}
		// A ceiling, a rate floor or a memory reserve only mean anything while
		// slots are being admitted dynamically. Saying one while switching the
		// mechanism off is a configuration that reads as if it does something.
		if c.Slots.Dynamic != nil && !*c.Slots.Dynamic &&
			(c.Slots.Max > 0 || c.Slots.TPSFloor > 0 || c.Slots.VRAMReserveMiB > 0) {
			return fmt.Errorf("xollama config: slots.max, slots.tps_floor and slots.vram_reserve_mib need slots.dynamic; they describe how slots are admitted")
		}
	}
	if c.DCA != nil {
		if c.DCA.ChunkSize < 0 {
			return fmt.Errorf("xollama config: dca.chunk_size %d must not be negative", c.DCA.ChunkSize)
		}
		// A chunk length describes how the chunked route splits positions, so
		// naming one while switching the route off reads as if it does
		// something. It does not.
		if c.DCA.Enabled != nil && !*c.DCA.Enabled && c.DCA.ChunkSize > 0 {
			return fmt.Errorf("xollama config: dca.chunk_size needs dca.enabled; it describes how the chunked route splits positions")
		}
	}
	if c.Session != nil {
		if c.Session.Pool != nil && *c.Session.Pool &&
			c.Session.Affinity != nil && !*c.Session.Affinity {
			return fmt.Errorf("xollama config: session.pool requires session.affinity; a shared prefix pool has nothing to attach to without session identity")
		}
		if c.Session.MaxPools < 0 {
			return fmt.Errorf("xollama config: session.max_pools %d must not be negative", c.Session.MaxPools)
		}
		// A count of prefixes to hold means nothing when nothing is being
		// pooled, and reads as if it switches pooling on. It does not.
		if c.Session.MaxPools > 0 && c.Session.Pool != nil && !*c.Session.Pool {
			return fmt.Errorf("xollama config: session.max_pools needs session.pool; it sizes the pooling that session.pool switches on")
		}
	}
	return nil
}

// Parse decodes and validates a xollama.json payload.
//
// Unknown fields are accepted: a future build may add one, and a model that
// merely mentions a setting this build does not have is still servable. An
// unknown VERSION is not accepted — see Validate.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("xollama config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Marshal renders the config for storage, stamping the schema version so a
// caller cannot write an unversioned blob by forgetting to set it.
// requiredVersion is the LOWEST schema that can express this config.
//
// Writing SchemaVersion unconditionally would make every model this build
// touches unreadable to an older xollama, including models that use nothing
// newer than v1 -- Validate treats a future version as an error, deliberately,
// because reading a newer config as if it were older runs the model
// differently from how its publisher meant. So a config states the oldest
// version that is true of it, and only a model that actually uses a v2 field
// pays the v2 floor.
func (c *Config) requiredVersion() int {
	if c.KV != nil && (c.KV.Unified != nil || c.KV.ResidencyMode != "") {
		return 2
	}
	return SchemaVersionBase
}

func (c *Config) Marshal() ([]byte, error) {
	out := *c
	// Recomputed, not defaulted: a config that was v2 and has since had its v2
	// fields cleared becomes readable by an older build again, which is the
	// whole point of stating the lowest true version.
	out.Version = out.requiredVersion()
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(&out)
}

// IsZero reports whether the config carries nothing worth storing, so the
// create path can skip writing an empty layer.
func (c *Config) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Engine == "" &&
		c.FlashAttention == "" &&
		(c.Draft == nil || c.Draft.SpecType == "") &&
		(c.KV == nil || (c.KV.K == "" && c.KV.V == "" && c.KV.KSWA == "" && c.KV.VSWA == "" &&
			c.KV.Unified == nil && c.KV.ResidencyMode == "")) &&
		(c.Slots == nil || (c.Slots.Dynamic == nil && c.Slots.Max == 0 && c.Slots.TPSFloor == 0 &&
			c.Slots.VRAMReserveMiB == 0 && c.Slots.SWASeqBudget == 0)) &&
		(c.DCA == nil || (c.DCA.Enabled == nil && c.DCA.ChunkSize == 0)) &&
		(c.Session == nil || (c.Session.Affinity == nil && c.Session.Pool == nil && c.Session.MaxPools == 0))
}

// The closed sets, exported so a tool that ASKS for one of these values offers
// exactly what Validate will accept. A menu built from a second hand-written
// list is a menu that drifts; `xollama tweak model` builds its from these.
//
// Engines and cache types are deliberately not both here: the engine set is
// closed and returned, while cache types are not validated at all (see KV),
// because the set a build accepts depends on the engine that serves the load.

// ValidEngines returns the engines a model may pin.
func ValidEngines() []string { return slices.Clone(validEngines) }

// ValidResidencyModes returns the rolling-KV tactics the engine's own parser
// accepts.
func ValidResidencyModes() []string { return slices.Clone(validResidencyModes) }

// ValidFlashAttention returns what llama-server's --flash-attn accepts.
func ValidFlashAttention() []string { return slices.Clone(validFlashAttention) }

// ValidSpecTypes returns the speculative-decoding types a model may pin.
func ValidSpecTypes() []string { return slices.Clone(validSpecTypes) }
