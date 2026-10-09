package xollama

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Server defaults (plans/system-settings.md): the settings a server applies
// to every model that does not state them itself.
//
// Precedence is the model, then the server's defaults, then the environment,
// then the built-in default. The model wins because its publisher, or its
// owner through `xollama tweak model`, chose for that model; the server's
// default is the operator's choice for every model that did not.

// defaultSections are the parts of a model's config a server may default, in
// the order they are applied. The rest are the model's own: a council is what
// the model is, a device pin is per model by design (devices.go), and DCA's
// chunk is a property of the model's training.
var defaultSections = []string{"engine", "flash_attention", "kv", "slots", "session", "draft", "fit"}

// DefaultSections returns the config's top-level keys a server may default.
func DefaultSections() []string { return slices.Clone(defaultSections) }

// ValidateDefaults says whether c can be a server's defaults: valid as a
// config, and stating nothing outside DefaultSections.
func (c *Config) ValidateDefaults() error {
	if c == nil {
		return nil
	}
	if c.DCA != nil || !c.Devices.IsZero() || c.Council != nil {
		return fmt.Errorf("xollama config: dca, devices and council are a model's own settings, not a server default")
	}
	if c.Draft != nil && c.Draft.Head != "" {
		return fmt.Errorf("xollama config: draft.head is a model's own drafter, not a server default")
	}
	probe := *c
	probe.Version = probe.requiredVersion()
	return probe.Validate()
}

// WithDefaults returns the config a model runs with: c, with every section of
// d that c does not state filled in from d, key by key inside a section.
//
// A section the merge would make invalid for this model is left out whole,
// and named in skipped: a server default of a KVarN cache does not apply to a
// model pinned to stock llama.cpp, and serving that model is not a reason to
// refuse it. With no defaults c itself is returned, so a server without
// defaults runs every model exactly as before.
func (c *Config) WithDefaults(d *Config) (out *Config, skipped []string) {
	if d.IsZero() {
		return c, nil
	}
	base, err := toMap(c)
	if err != nil {
		return c, nil
	}
	defs, err := toMap(d)
	if err != nil {
		return c, nil
	}

	current := c
	for _, section := range defaultSections {
		dv, ok := defs[section]
		if !ok {
			continue
		}
		candidate := make(map[string]any, len(base)+1)
		for k, v := range base {
			candidate[k] = v
		}
		candidate[section] = mergeUnder(base[section], keepPairs(section, base[section], dv))
		merged, err := fromMap(candidate)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", section, err))
			continue
		}
		base, current = candidate, merged
	}
	return current, skipped
}

// mergeUnder returns own with every key it lacks taken from def. A value
// that is not an object is own's when own states it at all.
func mergeUnder(own, def any) any {
	if own == nil {
		return def
	}
	om, ok1 := own.(map[string]any)
	dm, ok2 := def.(map[string]any)
	if !ok1 || !ok2 {
		return own
	}
	out := make(map[string]any, len(om)+len(dm))
	for k, v := range dm {
		out[k] = v
	}
	for k, v := range om {
		out[k] = mergeUnder(v, dm[k])
	}
	return out
}

func toMap(c *Config) (map[string]any, error) {
	m := map[string]any{}
	if c == nil {
		return m, nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	delete(m, "version")
	return m, nil
}

func fromMap(m map[string]any) (*Config, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.Devices.normalize()
	c.Version = c.requiredVersion()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// cachePairs are KV keys that only make sense together. A model that states
// one half of a pair chose that pair: the server's other half would build a
// cache nobody configured, so the default's pair is left out whole.
var cachePairs = [][2]string{{"k", "v"}, {"k_swa", "v_swa"}}

func keepPairs(section string, own, def any) any {
	om, ok1 := own.(map[string]any)
	dm, ok2 := def.(map[string]any)
	if section != "kv" || !ok1 || !ok2 {
		return def
	}
	out := make(map[string]any, len(dm))
	for k, v := range dm {
		out[k] = v
	}
	for _, p := range cachePairs {
		if _, a := om[p[0]]; a {
			delete(out, p[1])
		} else if _, b := om[p[1]]; b {
			delete(out, p[0])
		}
	}
	return out
}
