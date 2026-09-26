package xollama

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
)

// Council makes a model a council: one model name that, on every chat turn,
// runs a planner, researchers in parallel, critics in parallel and a
// synthesizer, and streams back one answer. Clients ask for the model like
// any other; the council runs inside the server. The plan is
// plans/agentic-council-chat.md.
//
// Everything here is optional except the switch. The defaults are the ones
// the plan measured: 2 researchers, 2 critics, a random seed per member and a
// ±2 % temperature spread on researchers and critics, one round, deliberation
// shown as thinking. A model that states only `enabled: true` gets all of it.
//
// Schema v4. An older build reads `council` as an unknown field and serves the
// model as a plain chat, which is serving it differently from how its
// publisher meant -- so a council raises the floor (see requiredVersion).
type Council struct {
	// Enabled switches the council on. Nil or false is an ordinary model.
	// There is no environment fallback: a council is a property of the model,
	// never of the server.
	Enabled *bool `json:"enabled,omitempty"`

	// Charter replaces the built-in system prompt every member shares. It is
	// the council's shared prefix, so under PolyKV it is prefilled once per
	// conversation, not once per member.
	Charter string `json:"charter,omitempty"`

	// The roles. Count applies to researchers and critics only: there is one
	// planner and one synthesizer.
	Planner     *CouncilRole `json:"planner,omitempty"`
	Researcher  *CouncilRole `json:"researcher,omitempty"`
	Critic      *CouncilRole `json:"critic,omitempty"`
	Synthesizer *CouncilRole `json:"synthesizer,omitempty"`

	// TemperatureJitter is the relative spread drawn around the model's
	// temperature for each researcher and critic: 0.02 draws from
	// [T·0.98, T·1.02]. Nil is the default 0.02; a stated 0 means no spread,
	// which is why this is a pointer.
	TemperatureJitter *float64 `json:"temperature_jitter,omitempty"`

	// Seed, when stated, makes a council reproducible: every member's seed is
	// derived from it. Nil draws a fresh random seed per member per request.
	Seed *int64 `json:"seed,omitempty"`

	// MaxRounds bounds the critic→researcher loop: a critic may send the
	// research back this many times minus one. Zero means 1, no loop.
	MaxRounds int `json:"max_rounds,omitempty"`

	// ShowDeliberation streams the planner's plan, the findings and the
	// critiques as thinking. Nil means on; off hides them and leaves the
	// answer unchanged.
	ShowDeliberation *bool `json:"show_deliberation,omitempty"`

	// PolyKV is whether the members share the conversation's KV through
	// opencoti's pools: "auto" (the default: on when the engine advertises
	// it), "on" (refuse to serve without it) or "off".
	PolyKV string `json:"polykv,omitempty"`

	// Context sizes the council's engine session and says when to compact.
	Context *CouncilContext `json:"context,omitempty"`
}

// CouncilRole is one role's settings. Unstated fields take the defaults.
type CouncilRole struct {
	// Count is how many members of this role run in parallel. Researchers and
	// critics only; zero means the default, 2.
	Count int `json:"count,omitempty"`

	// Model serves this role instead of the council's own model. Empty means
	// the council's model, which is the case PolyKV can share: members on a
	// different model share nothing with the rest.
	Model string `json:"model,omitempty"`

	// Host serves this role on another ollama or xollama server, as a URL
	// ("http://gpu2:11434"), with Model named as that server names it. Empty
	// means this server, which also reaches cloud models (a Model ending in
	// ":cloud"). The server serves a host only when XOLLAMA_COUNCIL_HOSTS
	// allows it: a council model can be pulled, and its host would receive
	// every conversation. A remote member shares no cache. A researcher or
	// critic whose host (or other model) fails is answered by the council's own
	// model instead; a planner or synthesizer that fails fails the turn.
	Host string `json:"host,omitempty"`

	// Prompt replaces the role's built-in instruction.
	Prompt string `json:"prompt,omitempty"`

	// MaxTokens caps one member's reply. Zero means the role's default.
	MaxTokens int `json:"max_tokens,omitempty"`

	// Think lets the role's members reason before they reply. Empty or "off"
	// is the default: no reasoning. "on" is a budget of
	// DefaultCouncilThinkBudget tokens. A level (minimal, low, medium, high,
	// max) caps the reasoning at that share of the member's context window, as
	// a think level does for a chat request; a positive integer is a token
	// budget. The cap is added to max_tokens, so
	// the reply keeps its own room. The reasoning is never shown: only the
	// reply joins the deliberation. The planner's routing call never reasons.
	// When the cap is reached, the model's own think_budget_message closes
	// the reasoning.
	Think string `json:"think,omitempty"`
}

// CouncilContext is the council's window and its compaction trigger. The
// engine grants windows and reports pressure (/kv, kv_pressure_v1); these say
// what to ask for and when to compact.
type CouncilContext struct {
	// Window is the num_ctx the council's owner session asks for. Zero means
	// the model's own context length. The engine may grant less; under
	// kv_pressure_v1 an idle owner gives some back and asks again once the
	// pressure is gone (kv_resize_v1).
	Window int `json:"window,omitempty"`

	// Floor is the smallest window the council accepts (num_ctx_min). Zero
	// means all or nothing.
	Floor int `json:"floor,omitempty"`

	// CompactAt is the share of the granted window at which the conversation
	// is compacted before the next turn, in (0, 1). Zero means the default
	// 0.85.
	CompactAt float64 `json:"compact_at,omitempty"`

	// IdleCompactAt is the owner pressure at which the conversation is
	// summarised after an answer, while the council waits for the next
	// message, so that message starts from the short conversation. In (0, 1)
	// and not above compact_at; zero means the default 0.75. PolyKV only: it
	// reads the owner's /kv row.
	IdleCompactAt float64 `json:"idle_compact_at,omitempty"`
}

// Council role names, the keys a tool or a message uses for them.
const (
	RolePlanner     = "planner"
	RoleResearcher  = "researcher"
	RoleCritic      = "critic"
	RoleSynthesizer = "synthesizer"
)

// PolyKV settings a council may state.
const (
	CouncilPolyKVAuto = "auto"
	CouncilPolyKVOn   = "on"
	CouncilPolyKVOff  = "off"
)

var validCouncilPolyKV = []string{CouncilPolyKVAuto, CouncilPolyKVOn, CouncilPolyKVOff}

// Council think settings besides a positive token count. The levels are the
// ones a chat request's think accepts.
const (
	CouncilThinkOff = "off"
	CouncilThinkOn  = "on"
)

// Compaction thresholds a council takes when its context states none: the
// owner session's pressure at which a turn compacts, and at which an idle
// council compacts after its answer.
const (
	DefaultCouncilCompactAt     = 0.85
	DefaultCouncilIdleCompactAt = 0.75
)

// DefaultCouncilThinkBudget is what `think: on` gives a member. A level would
// be a share of the council's context, and at 131k "medium" is 32,768 tokens
// per member: measured live, a researcher looped past 29k of them.
const DefaultCouncilThinkBudget = 2048

var validCouncilThink = []string{CouncilThinkOff, CouncilThinkOn, "minimal", "low", "medium", "high", "max"}

// ValidCouncilThink returns the named think settings a role may state; a
// positive integer token budget is also valid.
func ValidCouncilThink() []string { return slices.Clone(validCouncilThink) }

// ValidCouncilThinkValue reports whether v is a think setting a role may state.
func ValidCouncilThinkValue(v string) bool {
	if v == "" || slices.Contains(validCouncilThink, v) {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0
}

// ValidCouncilPolyKV returns the PolyKV settings a council may state.
func ValidCouncilPolyKV() []string { return slices.Clone(validCouncilPolyKV) }

// Council limits. MaxCouncilWidth is the widest fan-out Phase 1 measured
// (plans/agentic-council-chat.md): every member is a concurrent request, and a
// width the server cannot run in parallel only queues. MaxCouncilRounds keeps
// a critic that never approves from running a turn forever.
const (
	MaxCouncilWidth  = 8
	MaxCouncilRounds = 4
	// MaxCouncilJitter keeps the spread a spread: past half the temperature
	// the members are no longer the same model at slightly different heat.
	MaxCouncilJitter = 0.5
)

// Role returns the named role's settings, or nil.
func (c *Council) Role(name string) *CouncilRole {
	if c == nil {
		return nil
	}
	switch name {
	case RolePlanner:
		return c.Planner
	case RoleResearcher:
		return c.Researcher
	case RoleCritic:
		return c.Critic
	case RoleSynthesizer:
		return c.Synthesizer
	}
	return nil
}

// On reports whether the council is switched on.
func (c *Council) On() bool { return c != nil && c.Enabled != nil && *c.Enabled }

// IsZero reports whether the council states nothing.
func (c *Council) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Enabled == nil && c.Charter == "" &&
		c.Planner.isZero() && c.Researcher.isZero() && c.Critic.isZero() && c.Synthesizer.isZero() &&
		c.TemperatureJitter == nil && c.Seed == nil && c.MaxRounds == 0 &&
		c.ShowDeliberation == nil && c.PolyKV == "" && c.Context.isZero()
}

func (r *CouncilRole) isZero() bool { return r == nil || *r == (CouncilRole{}) }

func (x *CouncilContext) isZero() bool { return x == nil || *x == (CouncilContext{}) }

// validate checks the council against itself and against the engine the
// model pins.
func (c *Council) validate(engine string) error {
	if c.IsZero() {
		return nil
	}
	// Settings for a council that is not switched on read as if they do
	// something. They do not -- and a model that states them without the
	// switch was probably meant to be a council.
	// The switch alone, off, is an answer: "this model is not a council",
	// which overrides a parent that is one.
	settings := *c
	settings.Enabled = nil
	if !c.On() && !settings.IsZero() {
		return fmt.Errorf("xollama config: council settings need council.enabled; they describe a council this model does not run")
	}
	for _, r := range []struct {
		name string
		role *CouncilRole
	}{{RolePlanner, c.Planner}, {RoleResearcher, c.Researcher}, {RoleCritic, c.Critic}, {RoleSynthesizer, c.Synthesizer}} {
		if r.role == nil {
			continue
		}
		if r.role.Count < 0 || r.role.MaxTokens < 0 {
			return fmt.Errorf("xollama config: council.%s: count and max_tokens must not be negative", r.name)
		}
		if r.role.Count > 0 && (r.name == RolePlanner || r.name == RoleSynthesizer) {
			return fmt.Errorf("xollama config: council.%s.count: there is one %s; count applies to researchers and critics", r.name, r.name)
		}
		if r.role.Host != "" {
			u, err := url.Parse(r.role.Host)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("xollama config: council.%s.host %q: want an http or https URL, such as http://gpu2:11434", r.name, r.role.Host)
			}
			// The council's own name on another xollama could be a council
			// too, and would convene there; the remote model must be named.
			if r.role.Model == "" {
				return fmt.Errorf("xollama config: council.%s.host needs council.%s.model, the model as %s names it", r.name, r.name, u.Host)
			}
		}
		if !ValidCouncilThinkValue(r.role.Think) {
			return fmt.Errorf("xollama config: council.%s.think %q: want one of %v or a positive token count", r.name, r.role.Think, validCouncilThink)
		}
		if r.role.Count > MaxCouncilWidth {
			return fmt.Errorf("xollama config: council.%s.count %d is above %d; every member is a concurrent request", r.name, r.role.Count, MaxCouncilWidth)
		}
	}
	if j := c.TemperatureJitter; j != nil && (*j < 0 || *j > MaxCouncilJitter) {
		return fmt.Errorf("xollama config: council.temperature_jitter %v must be in [0, %v]; it is a relative spread", *j, MaxCouncilJitter)
	}
	if c.Seed != nil && *c.Seed < 0 {
		return fmt.Errorf("xollama config: council.seed %d must not be negative", *c.Seed)
	}
	if c.MaxRounds < 0 || c.MaxRounds > MaxCouncilRounds {
		return fmt.Errorf("xollama config: council.max_rounds %d must be in [0, %d]", c.MaxRounds, MaxCouncilRounds)
	}
	if c.PolyKV != "" && !slices.Contains(validCouncilPolyKV, c.PolyKV) {
		return fmt.Errorf("xollama config: unknown council.polykv %q (want one of %v)", c.PolyKV, validCouncilPolyKV)
	}
	// PolyKV is opencoti's. Requiring it while pinning the stock engine is two
	// incompatible answers; refuse it here rather than at the first request.
	if c.PolyKV == CouncilPolyKVOn && engine == EngineLlamaCpp {
		return fmt.Errorf("xollama config: council.polykv on needs the opencoti engine; this config pins engine %q", engine)
	}
	if x := c.Context; x != nil {
		if x.Window < 0 || x.Floor < 0 {
			return fmt.Errorf("xollama config: council.context.window and floor must not be negative")
		}
		if x.Window > 0 && x.Floor > x.Window {
			return fmt.Errorf("xollama config: council.context.floor %d is above window %d; the engine grants a window in [floor, window]", x.Floor, x.Window)
		}
		if x.CompactAt < 0 || x.CompactAt >= 1 {
			return fmt.Errorf("xollama config: council.context.compact_at %v must be in (0, 1); it is the share of the council's window at which the conversation is compacted", x.CompactAt)
		}
		if x.IdleCompactAt < 0 || x.IdleCompactAt >= 1 {
			return fmt.Errorf("xollama config: council.context.idle_compact_at %v must be in (0, 1); it is the share of the council's window at which an idle council compacts", x.IdleCompactAt)
		}
		compactAt := x.CompactAt
		if compactAt == 0 {
			compactAt = DefaultCouncilCompactAt
		}
		if x.IdleCompactAt > compactAt {
			return fmt.Errorf("xollama config: council.context.idle_compact_at %v is above compact_at; the idle council compacts earlier than a turn does, not later", x.IdleCompactAt)
		}
	}
	return nil
}

// Clone returns a deep copy, so a tool can try a change on the copy and throw
// it away without the original seeing any of it.
func (c *Council) Clone() *Council {
	if c == nil {
		return nil
	}
	out := *c
	out.Enabled = clonePtr(c.Enabled)
	out.ShowDeliberation = clonePtr(c.ShowDeliberation)
	out.TemperatureJitter = clonePtr(c.TemperatureJitter)
	out.Seed = clonePtr(c.Seed)
	out.Planner = clonePtr(c.Planner)
	out.Researcher = clonePtr(c.Researcher)
	out.Critic = clonePtr(c.Critic)
	out.Synthesizer = clonePtr(c.Synthesizer)
	out.Context = clonePtr(c.Context)
	return &out
}

// Prune drops the parts that state nothing -- empty roles, an empty context,
// and the whole council when it is empty -- so what is stored matches what a
// review printed. It returns nil for a council that states nothing.
func (c *Council) Prune() *Council {
	if c == nil {
		return nil
	}
	if c.Planner.isZero() {
		c.Planner = nil
	}
	if c.Researcher.isZero() {
		c.Researcher = nil
	}
	if c.Critic.isZero() {
		c.Critic = nil
	}
	if c.Synthesizer.isZero() {
		c.Synthesizer = nil
	}
	if c.Context.isZero() {
		c.Context = nil
	}
	if c.IsZero() {
		return nil
	}
	return c
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// LaunchConfig is the part of the config that decides how the model is
// LOADED, which is everything but the council: a council changes how a turn
// is answered, never the engine's command line.
//
// The scheduler compares launch configs to decide whether a loaded runner can
// serve a request. Comparing the whole config would give a council tag built
// FROM a plain model its own runner -- a second copy of the weights -- and
// reload a model for an edited prompt. A config that states nothing else is
// nil, so it compares equal to a model with no config; the version is the
// rest's own, so a v4 council over v1 settings compares equal to those
// settings.
func (c *Config) LaunchConfig() *Config {
	if c == nil || c.Council == nil {
		return c
	}
	out := *c
	out.Council = nil
	if out.IsZero() {
		return nil
	}
	out.Version = out.requiredVersion()
	return &out
}
