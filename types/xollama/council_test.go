package xollama

import (
	"encoding/json"
	"strings"
	"testing"
)

func on() *bool { t := true; return &t }

// The owner's council: the switch alone is a whole configuration, because
// every default is the measured one.
func TestACouncilNeedsOnlyTheSwitch(t *testing.T) {
	c, err := Parse([]byte(`{"version":4,"council":{"enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Council.On() {
		t.Fatal("council.enabled true did not switch the council on")
	}
}

func TestAFullCouncilRoundTrips(t *testing.T) {
	jitter, seed := 0.0, int64(7)
	in := &Config{Council: &Council{
		Enabled:           on(),
		Charter:           "You are a council.",
		Planner:           &CouncilRole{MaxTokens: 512},
		Researcher:        &CouncilRole{Count: 3, Model: "qwen3:4b", Prompt: "Dig.", MaxTokens: 384},
		Critic:            &CouncilRole{Count: 2, MaxTokens: 256},
		Synthesizer:       &CouncilRole{Prompt: "Answer."},
		TemperatureJitter: &jitter,
		Seed:              &seed,
		MaxRounds:         2,
		ShowDeliberation:  new(bool),
		PolyKV:            CouncilPolyKVAuto,
		Context:           &CouncilContext{Window: 32768, Floor: 8192, CompactAt: 0.85},
	}}
	b, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(Marshal(x)) = %v\n%s", err, b)
	}
	if out.Version != 4 {
		t.Fatalf("a council was written as v%d, want 4", out.Version)
	}
	a, _ := json.Marshal(in.Council)
	z, _ := json.Marshal(out.Council)
	if string(a) != string(z) {
		t.Fatalf("council changed through the layer:\n in %s\nout %s", a, z)
	}
	// A stated zero jitter is "no spread", not "unstated": it must survive.
	if out.Council.TemperatureJitter == nil || *out.Council.TemperatureJitter != 0 {
		t.Fatal("temperature_jitter 0 was dropped; it means no spread, and nil means the default")
	}
	if out.Council.ShowDeliberation == nil || *out.Council.ShowDeliberation {
		t.Fatal("show_deliberation off was dropped")
	}
}

// An older build would serve a council model as a plain chat. The version is
// what makes it refuse instead, so a council must raise it -- and only a
// council.
func TestACouncilRaisesTheSchemaToV4AndOnlyACouncil(t *testing.T) {
	c := &Config{Engine: EngineOpencoti, Council: &Council{Enabled: on()}}
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version":4`) {
		t.Fatalf("council written as %s, want version 4", b)
	}
	c.Council = nil
	if b, _ = c.Marshal(); !strings.Contains(string(b), `"version":1`) {
		t.Fatalf("without the council the config should drop back to v1, got %s", b)
	}
}

func TestIsZeroSeesTheCouncil(t *testing.T) {
	if !(&Config{Council: &Council{}}).IsZero() {
		t.Fatal("an empty council section is nothing to store")
	}
	if !(&Config{Council: &Council{Researcher: &CouncilRole{}, Context: &CouncilContext{}}}).IsZero() {
		t.Fatal("empty roles and an empty context are nothing to store")
	}
	if (&Config{Council: &Council{Enabled: new(bool)}}).IsZero() {
		t.Fatal("council.enabled false is a stated answer, not nothing")
	}
	if (&Config{Council: &Council{Enabled: on(), Seed: new(int64)}}).IsZero() {
		t.Fatal("a stated seed of 0 is a stated answer")
	}
}

func TestValidateCouncil(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"settings without the switch", `{"council":{"researcher":{"count":3}}}`, "need council.enabled"},
		{"settings with the switch off", `{"council":{"enabled":false,"max_rounds":2}}`, "need council.enabled"},
		{"a count on the planner", `{"council":{"enabled":true,"planner":{"count":2}}}`, "there is one planner"},
		{"a count on the synthesizer", `{"council":{"enabled":true,"synthesizer":{"count":2}}}`, "there is one synthesizer"},
		{"too wide", `{"council":{"enabled":true,"researcher":{"count":9}}}`, "above 8"},
		{"a negative count", `{"council":{"enabled":true,"critic":{"count":-1}}}`, "must not be negative"},
		{"a negative max_tokens", `{"council":{"enabled":true,"critic":{"max_tokens":-1}}}`, "must not be negative"},
		{"jitter past half", `{"council":{"enabled":true,"temperature_jitter":0.6}}`, "temperature_jitter 0.6"},
		{"negative jitter", `{"council":{"enabled":true,"temperature_jitter":-0.01}}`, "temperature_jitter"},
		{"negative seed", `{"council":{"enabled":true,"seed":-1}}`, "council.seed"},
		{"too many rounds", `{"council":{"enabled":true,"max_rounds":5}}`, "max_rounds 5"},
		{"unknown polykv", `{"council":{"enabled":true,"polykv":"maybe"}}`, "unknown council.polykv"},
		{"polykv on the stock engine", `{"engine":"llamacpp","council":{"enabled":true,"polykv":"on"}}`, "needs the opencoti engine"},
		{"floor above window", `{"council":{"enabled":true,"context":{"window":8192,"floor":16384}}}`, "floor 16384 is above window 8192"},
		{"compact_at of 1", `{"council":{"enabled":true,"context":{"compact_at":1}}}`, "compact_at 1"},
		{"negative window", `{"council":{"enabled":true,"context":{"window":-1}}}`, "must not be negative"},
		{"unknown think", `{"council":{"enabled":true,"critic":{"think":"maybe"}}}`, `council.critic.think "maybe"`},
		{"a think budget of 0", `{"council":{"enabled":true,"researcher":{"think":"0"}}}`, "council.researcher.think"},
		{"a negative think budget", `{"council":{"enabled":true,"planner":{"think":"-5"}}}`, "council.planner.think"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(`{"version":4,` + tt.in[1:]))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse(%s) = %v, want an error containing %q", tt.in, err, tt.want)
			}
		})
	}

	// And what must be accepted: auto and off on the stock engine (the
	// council still runs, members just prefill their own copies), a floor
	// without a window (window 0 is the model's own), the widest council.
	for _, in := range []string{
		`{"engine":"llamacpp","council":{"enabled":true,"polykv":"auto"}}`,
		`{"engine":"llamacpp","council":{"enabled":true,"polykv":"off"}}`,
		`{"council":{"enabled":true,"context":{"floor":8192}}}`,
		`{"council":{"enabled":true,"researcher":{"count":8},"critic":{"count":8},"max_rounds":4,"temperature_jitter":0.5}}`,
		`{"council":{"enabled":false}}`,
		`{"council":{"enabled":true,"planner":{"think":"on"},"researcher":{"think":"medium"},"critic":{"think":"off"},"synthesizer":{"think":"2048"}}}`,
	} {
		if _, err := Parse([]byte(`{"version":4,` + in[1:])); err != nil {
			t.Errorf("Parse(%s) = %v, want it accepted", in, err)
		}
	}
}

// A v3 build reading a council must refuse it, not serve a plain chat: the
// version is the only thing it can see. This is that build's view.
func TestACouncilIsUnreadableToTheBuildBeforeIt(t *testing.T) {
	b, err := (&Config{Council: &Council{Enabled: on()}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version <= 3 {
		t.Fatalf("a council is written as v%d, which a v3 build would read and serve as a plain chat", v.Version)
	}
}

func TestRoleLooksUpByName(t *testing.T) {
	c := &Council{Researcher: &CouncilRole{Count: 3}}
	if c.Role(RoleResearcher).Count != 3 || c.Role(RoleCritic) != nil || c.Role("judge") != nil {
		t.Fatal("Role did not return the named role")
	}
	var nilc *Council
	if nilc.Role(RolePlanner) != nil || nilc.On() {
		t.Fatal("a nil council states nothing")
	}
}

func TestCloneSharesNothing(t *testing.T) {
	j, s := 0.02, int64(1)
	a := &Council{
		Enabled: on(), Researcher: &CouncilRole{Count: 2}, TemperatureJitter: &j, Seed: &s,
		Context: &CouncilContext{Window: 1024},
	}
	b := a.Clone()
	*b.Enabled, b.Researcher.Count, *b.TemperatureJitter, *b.Seed, b.Context.Window = false, 5, 0.1, 9, 2048
	if !*a.Enabled || a.Researcher.Count != 2 || *a.TemperatureJitter != 0.02 || *a.Seed != 1 || a.Context.Window != 1024 {
		t.Fatalf("a change to the clone reached the original: %+v", a)
	}
}

func TestPruneDropsWhatStatesNothing(t *testing.T) {
	c := (&Council{Enabled: on(), Planner: &CouncilRole{}, Context: &CouncilContext{}}).Prune()
	if c == nil || c.Planner != nil || c.Context != nil {
		t.Fatalf("Prune kept empty parts: %+v", c)
	}
	if (&Council{Researcher: &CouncilRole{}}).Prune() != nil {
		t.Fatal("a council stating nothing should prune to nil")
	}
}

func TestTheLaunchConfigLeavesTheCouncilOut(t *testing.T) {
	yes := true
	onlyCouncil := &Config{Version: 4, Council: &Council{Enabled: &yes}}
	if onlyCouncil.LaunchConfig() != nil {
		t.Fatal("a config stating only a council launches exactly like no config")
	}
	withKV := &Config{Version: 4, KV: &KV{K: "q8_0", V: "q8_0"}, Council: &Council{Enabled: &yes}}
	lc := withKV.LaunchConfig()
	if lc == nil || lc.Council != nil || lc.KV.K != "q8_0" || lc.Version != 1 {
		t.Fatalf("launch config = %+v; want the KV, no council, and the KV's own v1", lc)
	}
	if withKV.Council == nil {
		t.Fatal("LaunchConfig changed the config it was called on")
	}
	plain := &Config{Version: 1, KV: &KV{K: "q8_0", V: "q8_0"}}
	if plain.LaunchConfig() != plain {
		t.Fatal("a config with no council is its own launch config")
	}
}
