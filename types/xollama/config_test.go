package xollama

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Config
	}{
		{
			name: "version only",
			in:   `{"version":1}`,
			want: Config{Version: 1},
		},
		{
			name: "engine pin",
			in:   `{"version":1,"engine":"opencoti"}`,
			want: Config{Version: 1, Engine: "opencoti"},
		},
		{
			name: "draft spec type",
			in:   `{"version":1,"draft":{"spec_type":"draft-assistant"}}`,
			want: Config{Version: 1, Draft: &Draft{SpecType: "draft-assistant"}},
		},
		{
			// A newer build may add fields. A model mentioning one this build
			// does not have is still servable, so unknown KEYS are accepted.
			name: "unknown field is accepted",
			in:   `{"version":1,"engine":"llamacpp","something_new":{"a":1}}`,
			want: Config{Version: 1, Engine: "llamacpp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.want.Version || got.Engine != tt.want.Engine {
				t.Fatalf("Parse = %+v, want %+v", *got, tt.want)
			}
			if (got.Draft == nil) != (tt.want.Draft == nil) {
				t.Fatalf("Parse draft = %+v, want %+v", got.Draft, tt.want.Draft)
			}
			if got.Draft != nil && got.Draft.SpecType != tt.want.Draft.SpecType {
				t.Fatalf("Parse spec_type = %q, want %q", got.Draft.SpecType, tt.want.Draft.SpecType)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "no version", in: `{"engine":"opencoti"}`, wantErr: "missing or invalid version"},
		{name: "zero version", in: `{"version":0}`, wantErr: "missing or invalid version"},
		{name: "negative version", in: `{"version":-1}`, wantErr: "missing or invalid version"},
		{
			// The fields here change how a model is served. Reading a future
			// schema as if it were this one would serve the model differently
			// from how its publisher meant, and say nothing.
			name:    "newer schema",
			in:      `{"version":3}`,
			wantErr: "newer than this build understands",
		},
		{name: "unknown engine", in: `{"version":1,"engine":"vllm"}`, wantErr: `unknown engine "vllm"`},
		{name: "auto is not an engine", in: `{"version":1,"engine":"auto"}`, wantErr: `unknown engine "auto"`},
		{name: "unknown spec type", in: `{"version":1,"draft":{"spec_type":"draft-nope"}}`, wantErr: "unknown draft.spec_type"},
		{name: "not json", in: `{`, wantErr: "xollama config:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatalf("Parse(%s) succeeded, want error containing %q", tt.in, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Marshal stamps the version so a caller cannot write an unversioned blob by
// forgetting to set it — an unversioned blob is unreadable by Parse.
func TestMarshalStampsVersion(t *testing.T) {
	c := &Config{Engine: "opencoti"}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// The LOWEST version that expresses this config, not the newest this build
	// knows: a config using nothing past v1 must stay readable by a v1 build.
	if raw["version"] != float64(SchemaVersionBase) {
		t.Fatalf("marshalled version = %v, want %d", raw["version"], SchemaVersionBase)
	}
	if c.Version != 0 {
		t.Fatalf("Marshal mutated its receiver: version = %d, want 0", c.Version)
	}
	round, err := Parse(data)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if round.Engine != "opencoti" {
		t.Fatalf("round trip engine = %q, want opencoti", round.Engine)
	}
}

func TestMarshalRejectsInvalid(t *testing.T) {
	if _, err := (&Config{Engine: "nope"}).Marshal(); err == nil {
		t.Fatal("Marshal accepted an unknown engine")
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		name string
		c    *Config
		want bool
	}{
		{name: "nil", c: nil, want: true},
		{name: "empty", c: &Config{Version: 1}, want: true},
		{name: "empty draft struct", c: &Config{Version: 1, Draft: &Draft{}}, want: true},
		{name: "engine set", c: &Config{Version: 1, Engine: "opencoti"}, want: false},
		{name: "spec type set", c: &Config{Version: 1, Draft: &Draft{SpecType: "draft-mtp"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.IsZero(); got != tt.want {
				t.Fatalf("IsZero = %v, want %v", got, tt.want)
			}
		})
	}
}

// A shared prefix pool attaches to a session, so a model asking for a pool
// while switching session identity off is asking for something that cannot
// work. Catching it at create time is better than serving a model whose stated
// configuration silently does nothing.
func TestValidateSessionPoolRequiresAffinity(t *testing.T) {
	no, yes := false, true

	if err := (&Config{Version: 1, Session: &Session{Pool: &yes, Affinity: &no}}).Validate(); err == nil {
		t.Error("expected session.pool with session.affinity=false to be refused")
	}
	for _, tt := range []struct {
		name string
		s    *Session
	}{
		{"pool with affinity on", &Session{Pool: &yes, Affinity: &yes}},
		{"pool with affinity unstated, which leaves it to the server default", &Session{Pool: &yes}},
		{"affinity off on its own", &Session{Affinity: &no}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := (&Config{Version: 1, Session: tt.s}).Validate(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// A session block round-trips through the layer, and a config carrying only a
// session block is not "empty" -- writing no layer for it would drop the
// setting on publish.
func TestSessionRoundTripAndIsZero(t *testing.T) {
	yes := true
	c := &Config{Version: 1, Session: &Session{Affinity: &yes, Pool: &yes}}
	if c.IsZero() {
		t.Fatal("a config carrying a session block must be stored")
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Session == nil || got.Session.Affinity == nil || !*got.Session.Affinity ||
		got.Session.Pool == nil || !*got.Session.Pool {
		t.Errorf("session did not survive the round trip: %s", data)
	}
	if (&Config{Version: 1, Session: &Session{}}).IsZero() != true {
		t.Error("a session block stating nothing carries nothing worth storing")
	}
}

// The sliding-window ring is one cache with two halves. Setting one and leaving
// the other to a different default is not a configuration anyone means, and the
// engine refuses the pair at boot -- better to refuse it while the model is
// being created, where the offending line is visible.
func TestValidateKVRingPairing(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kv      *KV
		wantErr bool
	}{
		{"both halves of the ring", &KV{KSWA: "q4_0", VSWA: "q4_0"}, false},
		{"neither half", &KV{K: "kvarn3", V: "kvarn3"}, false},
		{"only the key half", &KV{KSWA: "q4_0"}, true},
		{"only the value half", &KV{VSWA: "q4_0"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Config{Version: 1, KV: tt.kv}).Validate()
			if tt.wantErr && err == nil {
				t.Error("expected the half-set ring to be refused")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Cache types are deliberately NOT validated against a list: the set a build
// accepts depends on which engine serves the load, and refusing an unknown name
// here would make a model published by a newer xollama fail to create on an
// older one. The refusal belongs at launch, where the engine is known.
func TestValidateAcceptsAnEngineSpecificCacheType(t *testing.T) {
	if err := (&Config{Version: 1, KV: &KV{K: "kvarn3", V: "kvarn3"}}).Validate(); err != nil {
		t.Errorf("an engine-specific cache type must be storable: %v", err)
	}
}

func TestKVRoundTripAndIsZero(t *testing.T) {
	c := &Config{Version: 1, KV: &KV{K: "q8_0", V: "q4_0", KSWA: "q4_0", VSWA: "q4_0"}}
	if c.IsZero() {
		t.Fatal("a config carrying cache types must be stored")
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.KV == nil || got.KV.K != "q8_0" || got.KV.V != "q4_0" || got.KV.KSWA != "q4_0" || got.KV.VSWA != "q4_0" {
		t.Errorf("kv did not survive the round trip: %s", data)
	}
	if !(&Config{Version: 1, KV: &KV{}}).IsZero() {
		t.Error("an empty kv block carries nothing worth storing")
	}
}

// A ceiling, a rate floor or a memory reserve describe how slots are admitted.
// Saying one while switching the mechanism off is a configuration that reads as
// if it does something, so it is refused where the line is visible.
func TestValidateSlots(t *testing.T) {
	off, on := false, true
	for _, tt := range []struct {
		name    string
		s       *Slots
		wantErr bool
	}{
		{"a ceiling with the mechanism on", &Slots{Dynamic: &on, Max: 8}, false},
		{"a ceiling with the mechanism unstated", &Slots{Max: 8}, false},
		{"switching it off on its own", &Slots{Dynamic: &off}, false},
		{"a ceiling with the mechanism off", &Slots{Dynamic: &off, Max: 8}, true},
		{"a rate floor with the mechanism off", &Slots{Dynamic: &off, TPSFloor: 10}, true},
		{"a reserve with the mechanism off", &Slots{Dynamic: &off, VRAMReserveMiB: 512}, true},
		{"a negative ceiling", &Slots{Max: -1}, true},
		{"a negative rate floor", &Slots{TPSFloor: -1}, true},
		{"a negative reserve", &Slots{VRAMReserveMiB: -1}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Config{Version: 1, Slots: tt.s}).Validate()
			if tt.wantErr && err == nil {
				t.Error("expected this to be refused")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSlotsRoundTripAndIsZero(t *testing.T) {
	on := true
	c := &Config{Version: 1, Slots: &Slots{Dynamic: &on, Max: 6, TPSFloor: 12.5, VRAMReserveMiB: 1024}}
	if c.IsZero() {
		t.Fatal("a config carrying slot settings must be stored")
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Slots == nil || got.Slots.Max != 6 || got.Slots.TPSFloor != 12.5 || got.Slots.VRAMReserveMiB != 1024 ||
		got.Slots.Dynamic == nil || !*got.Slots.Dynamic {
		t.Errorf("slots did not survive the round trip: %s", data)
	}
	if !(&Config{Version: 1, Slots: &Slots{}}).IsZero() {
		t.Error("an empty slots block carries nothing worth storing")
	}
}

func TestValidateDCA(t *testing.T) {
	on, off := true, false

	for _, tc := range []struct {
		name    string
		dca     *DCA
		wantErr string
	}{
		{name: "nothing said", dca: nil},
		{name: "on", dca: &DCA{Enabled: &on}},
		{name: "on with a chunk size", dca: &DCA{Enabled: &on, ChunkSize: 4096}},
		{name: "off", dca: &DCA{Enabled: &off}},
		{
			name:    "a negative chunk size",
			dca:     &DCA{Enabled: &on, ChunkSize: -1},
			wantErr: "must not be negative",
		},
		{
			// A chunk length describes how the chunked route splits positions.
			// Naming one while switching the route off reads as configuration
			// and is not.
			name:    "a chunk size with the route off",
			dca:     &DCA{Enabled: &off, ChunkSize: 4096},
			wantErr: "needs dca.enabled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&Config{Version: 1, DCA: tc.dca}).Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil, want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestDCARoundTripsThroughTheLayer(t *testing.T) {
	on := true
	data, err := (&Config{DCA: &DCA{Enabled: &on, ChunkSize: 8192}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.DCA == nil || got.DCA.Enabled == nil || !*got.DCA.Enabled || got.DCA.ChunkSize != 8192 {
		t.Errorf("round trip lost the DCA block: %+v", got.DCA)
	}

	// A config carrying only DCA still has something worth storing, or the
	// create path would drop the layer and the setting with it.
	if (&Config{DCA: &DCA{Enabled: &on}}).IsZero() {
		t.Error("a config that turns DCA on is not empty")
	}
}

func TestValidateFlashAttention(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{in: ""},
		{in: "on"},
		{in: "off"},
		{in: "auto"},
		// Unlike a cache type, this is a closed set: both engines take the same
		// three words, and a fourth would be rejected by the engine's own
		// parser long after the model had been published.
		{in: "yes", wantErr: true},
		{in: "true", wantErr: true},
		{in: "ON", wantErr: true},
	} {
		t.Run("flash_attention="+tc.in, func(t *testing.T) {
			err := (&Config{Version: 1, FlashAttention: tc.in}).Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want an error for %q", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil for %q", err, tc.in)
			}
		})
	}
}

func TestValidateSWASeqBudget(t *testing.T) {
	if err := (&Config{Version: 1, Slots: &Slots{SWASeqBudget: 4}}).Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	err := (&Config{Version: 1, Slots: &Slots{SWASeqBudget: -1}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("Validate() = %v, want a negative-value error", err)
	}
	// It sizes a cache rather than the slot mechanism, so unlike the other
	// slots fields it is allowed alongside dynamic slots being off.
	off := false
	if err := (&Config{Version: 1, Slots: &Slots{Dynamic: &off, SWASeqBudget: 2}}).Validate(); err != nil {
		t.Errorf("Validate() = %v, want a window budget to be allowed with fixed slots", err)
	}
	if (&Config{Slots: &Slots{SWASeqBudget: 2}}).IsZero() {
		t.Error("a config that sets a window budget is not empty")
	}
	if (&Config{FlashAttention: "off"}).IsZero() {
		t.Error("a config that pins flash attention is not empty")
	}
}

// A model that uses nothing newer than v1 must keep saying v1, or every model
// this build touches becomes unreadable to an older xollama for no reason.
func TestTheVersionWrittenIsTheLowestThatIsTrue(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		name string
		cfg  Config
		want int
	}{
		{"nothing from v2", Config{Engine: EngineOpencoti, KV: &KV{K: "q8_0", V: "q8_0"}}, SchemaVersionBase},
		{"slots are v1", Config{Slots: &Slots{Dynamic: &yes, Max: 8}}, SchemaVersionBase},
		{"unified is v2", Config{KV: &KV{Unified: &no}}, 2},
		{"residency mode is v2", Config{KV: &KV{ResidencyMode: ResidencyWindow}}, 2},
		{"unified true is still v2", Config{KV: &KV{Unified: &yes}}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.cfg.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			if raw["version"] != float64(tt.want) {
				t.Fatalf("version = %v, want %d", raw["version"], tt.want)
			}
		})
	}
}

// Clearing the v2 fields must take the config back to v1, which is the reason
// Marshal recomputes the version instead of defaulting it.
func TestClearingAV2FieldGoesBackToV1(t *testing.T) {
	no := false
	c := &Config{Version: 2, KV: &KV{K: "q8_0", Unified: &no}}
	c.KV.Unified = nil
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != float64(SchemaVersionBase) {
		t.Fatalf("version = %v, want %d after clearing the v2 field", raw["version"], SchemaVersionBase)
	}
}

func TestV2FieldsAreValidated(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"unknown residency mode", Config{KV: &KV{ResidencyMode: "sideways"}}, "unknown kv.residency_mode"},
		{"residency needs opencoti", Config{Engine: EngineLlamaCpp, KV: &KV{ResidencyMode: ResidencyHead}}, "needs the opencoti engine"},
		{"dynamic slots need the unified pool", Config{KV: &KV{Unified: &no}, Slots: &Slots{Dynamic: &yes}}, "slots.dynamic needs kv.unified"},
		{"a pool needs the unified pool", Config{KV: &KV{Unified: &no}, Session: &Session{Pool: &yes, Affinity: &yes}}, "session.pool needs kv.unified"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.Version = 2
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() succeeded, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	// The same pairs are fine when unified is not being refused.
	ok := Config{
		Version: 2, KV: &KV{Unified: &yes, ResidencyMode: ResidencyWindow},
		Slots: &Slots{Dynamic: &yes}, Session: &Session{Affinity: &yes, Pool: &yes},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a consistent v2 config was rejected: %v", err)
	}
}

// A config with a v2 field in it is not zero, or tweak would silently drop it.
func TestIsZeroSeesTheV2Fields(t *testing.T) {
	no := false
	for _, c := range []Config{
		{KV: &KV{Unified: &no}},
		{KV: &KV{ResidencyMode: ResidencyAuto}},
	} {
		if c.IsZero() {
			t.Fatalf("IsZero() = true for %+v", c.KV)
		}
	}
}
