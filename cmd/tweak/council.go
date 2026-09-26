package tweak

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ollama/ollama/types/xollama"
)

// The council rows. A council is a model that answers every chat turn with a
// planner, researchers and critics in parallel, and a synthesizer
// (plans/agentic-council-chat.md). Its settings are questions only once it is
// switched on, so every row but the switch is quiet while it is off.

func council(c *xollama.Config) *xollama.Council {
	if c.Council == nil {
		c.Council = &xollama.Council{}
	}
	return c.Council
}

func councilRole(c *xollama.Config, name string) *xollama.CouncilRole {
	k := council(c)
	slot := map[string]**xollama.CouncilRole{
		xollama.RolePlanner: &k.Planner, xollama.RoleResearcher: &k.Researcher,
		xollama.RoleCritic: &k.Critic, xollama.RoleSynthesizer: &k.Synthesizer,
	}[name]
	if *slot == nil {
		*slot = &xollama.CouncilRole{}
	}
	return *slot
}

func councilContext(c *xollama.Config) *xollama.CouncilContext {
	k := council(c)
	if k.Context == nil {
		k.Context = &xollama.CouncilContext{}
	}
	return k.Context
}

// councilOff blocks every council setting while the council is not on.
// Unlike offParent, unstated counts as off: there is no environment variable
// that could switch a council on, so a setting under an unset switch can
// never apply.
func councilOff(c *xollama.Config) string {
	if c.Council.On() {
		return ""
	}
	return "a council setting, and council.enabled is not on"
}

// setText keeps text as typed. `@path` reads it from a file, because a prompt
// is rarely one line.
func setText(v string, dst *string) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none":
		*dst = ""
		return nil
	}
	if path, ok := strings.CutPrefix(s, "@"); ok {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		s = strings.TrimSpace(string(b))
		if s == "" {
			return fmt.Errorf("%s is empty", path)
		}
	}
	*dst = s
	return nil
}

// setFloatPtr is setFloat for a setting where 0 is an answer, not "unset".
func setFloatPtr(v string, dst **float64) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "auto":
		*dst = nil
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("want a number or unset (got %q)", v)
	}
	if f < 0 {
		return fmt.Errorf("want a number that is not negative (got %q)", v)
	}
	*dst = &f
	return nil
}

// setSeed takes a seed, or `random` (the default) to clear it.
func setSeed(v string, dst **int64) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none", "random":
		*dst = nil
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("want a seed that is a whole number, or random (got %q)", v)
	}
	*dst = &n
	return nil
}

func councilGet(c *xollama.Config, f func(*xollama.Council) string) string {
	return orEmpty(c.Council != nil, func() string { return f(c.Council) })
}

func roleGet(name string, f func(*xollama.CouncilRole) string) func(*xollama.Config) string {
	return func(c *xollama.Config) string {
		r := c.Council.Role(name)
		return orEmpty(r != nil, func() string { return f(r) })
	}
}

// roleFields are the three settings every role has.
func roleFields(name, what string) []field {
	flag := "council-" + name
	return []field{
		{
			name:  flag + "-model",
			path:  "council." + name + ".model",
			title: "Council " + name + " model — serve the " + name + " with another model",
			help: "Unset uses the council's own model, which is the case PolyKV can share: a\n" +
				"member on a different model shares nothing with the rest and prefills the\n" +
				"whole conversation itself.",
			kind:    kindText,
			quiet:   true,
			blocked: councilOff,
			get:     roleGet(name, func(r *xollama.CouncilRole) string { return r.Model }),
			set: func(c *xollama.Config, v string) error {
				return setText(v, &councilRole(c, name).Model)
			},
		},
		{
			name:  flag + "-prompt",
			path:  "council." + name + ".prompt",
			title: "Council " + name + " prompt — replace the built-in instruction",
			help:  "The instruction the " + name + " is given for " + what + ". Unset keeps the\nbuilt-in one, which is the tested answer.",
			kind:  kindText,
			quiet: true,
			// Prompts are asked under --council-charter, not under --council.
			blocked: councilOff,
			get:     roleGet(name, func(r *xollama.CouncilRole) string { return r.Prompt }),
			set: func(c *xollama.Config, v string) error {
				return setText(v, &councilRole(c, name).Prompt)
			},
		},
		{
			name:    flag + "-max-tokens",
			path:    "council." + name + ".max_tokens",
			title:   "Council " + name + " reply cap",
			help:    "The most tokens one " + name + " may write. Unset keeps the role's default.",
			kind:    kindInt,
			unit:    "tokens",
			quiet:   true,
			blocked: councilOff,
			get:     roleGet(name, func(r *xollama.CouncilRole) string { return showInt(r.MaxTokens) }),
			set: func(c *xollama.Config, v string) error {
				return setInt(v, &councilRole(c, name).MaxTokens)
			},
		},
	}
}

func councilFields() []field {
	core := []field{
		{
			name:  "council",
			path:  "council.enabled",
			title: "Council — answer every turn with a council instead of one model call",
			help: "A planner decides whether a message needs the council: a greeting is answered\n" +
				"directly, a harder question goes to researchers in parallel, then critics in\n" +
				"parallel, then a synthesizer who writes the one answer the client receives.\n" +
				"Clients ask for this model like any other. The deliberation streams as\n" +
				"thinking. Unset or off is an ordinary model; there is no environment\n" +
				"variable for this -- a council is a property of the model.",
			kind:  kindTri,
			head:  true,
			group: []string{"council", "council-researchers", "council-critics", "council-jitter", "council-seed", "council-max-rounds", "council-show-deliberation", "council-polykv", "council-window", "council-floor", "council-compact-at"},
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string { return tri(k.Enabled) })
			},
			set: func(c *xollama.Config, v string) error { return setTri(v, &council(c).Enabled) },
		},
		{
			name:    "council-researchers",
			path:    "council.researcher.count",
			title:   "Researchers — how many investigate in parallel",
			help:    fmt.Sprintf("Each gets its own brief from the planner. Unset is 2. At most %d: every member is\na concurrent request.", xollama.MaxCouncilWidth),
			kind:    kindInt,
			unit:    "researchers",
			quiet:   true,
			blocked: councilOff,
			get:     roleGet(xollama.RoleResearcher, func(r *xollama.CouncilRole) string { return showInt(r.Count) }),
			set: func(c *xollama.Config, v string) error {
				return setInt(v, &councilRole(c, xollama.RoleResearcher).Count)
			},
		},
		{
			name:    "council-critics",
			path:    "council.critic.count",
			title:   "Critics — how many review the findings in parallel",
			help:    fmt.Sprintf("Each reviews the plan and every finding. Unset is 2. At most %d.", xollama.MaxCouncilWidth),
			kind:    kindInt,
			unit:    "critics",
			quiet:   true,
			blocked: councilOff,
			get:     roleGet(xollama.RoleCritic, func(r *xollama.CouncilRole) string { return showInt(r.Count) }),
			set: func(c *xollama.Config, v string) error {
				return setInt(v, &councilRole(c, xollama.RoleCritic).Count)
			},
		},
		{
			name:  "council-jitter",
			path:  "council.temperature_jitter",
			title: "Temperature spread for researchers and critics",
			help: "Each researcher and critic draws its temperature within this relative spread\n" +
				"of the model's: 0.02 draws from T·0.98 to T·1.02, so two members given the same\n" +
				"brief still differ. Unset is 0.02; 0 means no spread. Every member also gets\n" +
				"its own random seed.",
			kind:    kindFloat,
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string {
					if k.TemperatureJitter == nil {
						return ""
					}
					return strconv.FormatFloat(*k.TemperatureJitter, 'g', -1, 64)
				})
			},
			set: func(c *xollama.Config, v string) error { return setFloatPtr(v, &council(c).TemperatureJitter) },
		},
		{
			name:  "council-seed",
			path:  "council.seed",
			title: "Council seed — make every answer reproducible",
			help: "Unset draws a fresh random seed for every member of every request. A number\n" +
				"derives every member's seed from it, so the same question gets the same\n" +
				"council -- useful for testing prompts, not for serving.",
			kind:    kindInt,
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string {
					if k.Seed == nil {
						return ""
					}
					return strconv.FormatInt(*k.Seed, 10)
				})
			},
			set: func(c *xollama.Config, v string) error { return setSeed(v, &council(c).Seed) },
		},
		{
			name:    "council-max-rounds",
			path:    "council.max_rounds",
			title:   "Rounds — how often critics may send the research back",
			help:    fmt.Sprintf("1 (unset) runs research and critique once. More lets a critic that finds the\nfindings not good enough send them back for another round. At most %d.", xollama.MaxCouncilRounds),
			kind:    kindInt,
			unit:    "rounds",
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string { return showInt(k.MaxRounds) })
			},
			set: func(c *xollama.Config, v string) error { return setInt(v, &council(c).MaxRounds) },
		},
		{
			name:  "council-show-deliberation",
			path:  "council.show_deliberation",
			title: "Show the deliberation as thinking",
			help: "The plan, the findings and the critiques stream as thinking, tagged by member,\n" +
				"so any client that shows thinking shows the council at work. Off hides them;\n" +
				"the answer is the same. Unset is on.",
			kind:    kindTri,
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string { return tri(k.ShowDeliberation) })
			},
			set: func(c *xollama.Config, v string) error { return setTri(v, &council(c).ShowDeliberation) },
		},
		{
			name:  "council-polykv",
			path:  "council.polykv",
			title: "PolyKV — members share the conversation's KV",
			help: "With PolyKV the conversation is prefilled once and every member prefills only\n" +
				"its own role: measured, 29-77 tokens per member instead of the whole prompt.\n" +
				"auto (unset) uses it when the engine offers it; on refuses to serve without\n" +
				"it, and needs the opencoti engine; off never uses it.",
			kind:    kindChoice,
			choices: func(*xollama.Config) []string { return xollama.ValidCouncilPolyKV() },
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string { return k.PolyKV })
			},
			set: func(c *xollama.Config, v string) error {
				s, err := choice(v, xollama.ValidCouncilPolyKV())
				if err != nil {
					return err
				}
				council(c).PolyKV = s
				return nil
			},
		},
		{
			name:  "council-window",
			path:  "council.context.window",
			title: "Council window — the context the council's session asks for",
			help: "The engine grants a window once per conversation and keeps it, so this is\n" +
				"asked once. Unset asks for the model's own context length.",
			kind:    kindInt,
			unit:    "tokens",
			with:    []string{"council-floor"},
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return orEmpty(c.Council != nil && c.Council.Context != nil, func() string { return showInt(c.Council.Context.Window) })
			},
			set: func(c *xollama.Config, v string) error { return setInt(v, &councilContext(c).Window) },
		},
		{
			name:  "council-floor",
			path:  "council.context.floor",
			title: "Council floor — the smallest window it accepts",
			help: "When the server cannot grant the whole window, it grants the largest that\n" +
				"fits at or above this. Unset is all or nothing.",
			kind:    kindInt,
			unit:    "tokens",
			with:    []string{"council-window"},
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return orEmpty(c.Council != nil && c.Council.Context != nil, func() string { return showInt(c.Council.Context.Floor) })
			},
			set: func(c *xollama.Config, v string) error { return setInt(v, &councilContext(c).Floor) },
		},
		{
			name:  "council-compact-at",
			path:  "council.context.compact_at",
			title: "Compact at — the share of the window that compacts the conversation",
			help: "Once the conversation fills this share of the council's window, it is\n" +
				"compacted before the next turn: the oldest turns summarised, the last three\n" +
				"kept. Unset is 0.85. A number between 0 and 1.",
			kind:    kindFloat,
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return orEmpty(c.Council != nil && c.Council.Context != nil, func() string { return showFloat(c.Council.Context.CompactAt) })
			},
			set: func(c *xollama.Config, v string) error { return setFloat(v, &councilContext(c).CompactAt) },
		},
		{
			name:  "council-charter",
			path:  "council.charter",
			title: "Council charter — the system prompt every member shares",
			help: "Replaces the built-in charter that tells each member what the council is and\n" +
				"which role it plays. It is the council's shared prefix, prefilled once per\n" +
				"conversation under PolyKV. Unset keeps the built-in one. `--council-charter`\n" +
				"on its own asks for the charter and every role's prompt.",
			kind:    kindText,
			head:    true,
			group:   []string{"council-charter", "council-planner-prompt", "council-researcher-prompt", "council-critic-prompt", "council-synthesizer-prompt"},
			quiet:   true,
			blocked: councilOff,
			get: func(c *xollama.Config) string {
				return councilGet(c, func(k *xollama.Council) string { return k.Charter })
			},
			set: func(c *xollama.Config, v string) error { return setText(v, &council(c).Charter) },
		},
	}
	for _, r := range []struct{ name, what string }{
		{xollama.RolePlanner, "deciding the route and writing the plan"},
		{xollama.RoleResearcher, "investigating its brief"},
		{xollama.RoleCritic, "reviewing the findings"},
		{xollama.RoleSynthesizer, "writing the answer"},
	} {
		core = append(core, roleFields(r.name, r.what)...)
	}
	return core
}

// The council rows come last: they describe how a turn is answered, not how
// the model is loaded, and they are asked only once the switch is on.
func init() {
	fields = append(fields, councilFields()...)
}
