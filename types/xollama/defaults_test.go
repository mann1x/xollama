package xollama

import (
	"strings"
	"testing"
)

func bp(b bool) *bool { return &b }

func TestNoDefaultsLeaveTheModelAsItIs(t *testing.T) {
	own := &Config{Engine: EngineOpencoti}
	got, skipped := own.WithDefaults(nil)
	if got != own || skipped != nil {
		t.Fatal("no defaults changed the model's config")
	}
	got, _ = own.WithDefaults(&Config{})
	if got != own {
		t.Fatal("empty defaults changed the model's config")
	}
}

func TestTheModelWinsKeyByKey(t *testing.T) {
	own := &Config{Slots: &Slots{Max: 2}}
	def := &Config{
		FlashAttention: "on",
		Slots:          &Slots{Dynamic: bp(true), Max: 8},
		Session:        &Session{Pool: bp(true)},
	}
	got, skipped := own.WithDefaults(def)
	if len(skipped) > 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	if got.Slots.Max != 2 {
		t.Fatalf("slots.max = %d: the model's own 2 must win", got.Slots.Max)
	}
	if got.Slots.Dynamic == nil || !*got.Slots.Dynamic {
		t.Fatal("slots.dynamic was not taken from the server")
	}
	if got.FlashAttention != "on" || got.Session == nil || got.Session.Pool == nil {
		t.Fatalf("sections the model does not state were not filled in: %+v", got)
	}
	if own.Slots.Dynamic != nil || own.FlashAttention != "" {
		t.Fatal("the model's own config was modified")
	}
}

func TestAModelStatingOneCacheHalfKeepsItsPair(t *testing.T) {
	own := &Config{KV: &KV{K: "q8_0", V: "q8_0"}}
	def := &Config{KV: &KV{K: "q4_0", V: "q4_0", ResidencyMode: ResidencyHead}}
	got, _ := own.WithDefaults(def)
	if got.KV.K != "q8_0" || got.KV.V != "q8_0" {
		t.Fatalf("kv = %s/%s, want the model's q8_0 pair", got.KV.K, got.KV.V)
	}
	if got.KV.ResidencyMode != ResidencyHead {
		t.Fatal("an unpaired kv default was not applied")
	}
}

func TestADefaultTheModelCannotUseIsSkippedNotFatal(t *testing.T) {
	own := &Config{Engine: EngineLlamaCpp}
	def := &Config{KV: &KV{ResidencyMode: ResidencyHead}, FlashAttention: "on"}
	got, skipped := own.WithDefaults(def)
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "kv:") {
		t.Fatalf("skipped = %v, want the kv section named", skipped)
	}
	if got.KV != nil || got.FlashAttention != "on" {
		t.Fatalf("got %+v: the kv section must be left out and the rest applied", got)
	}
}

func TestAServerDefaultsOnlyWhatIsNotAModelsOwn(t *testing.T) {
	if err := (&Config{Engine: EngineOpencoti, KV: &KV{K: "q8_0", V: "q8_0"}}).ValidateDefaults(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Config{
		{DCA: &DCA{Enabled: bp(true)}},
		{Devices: &Devices{Backend: "CUDA"}},
		{Council: &Council{}},
		{Engine: "nonsense"},
	} {
		if c.ValidateDefaults() == nil {
			t.Errorf("%+v accepted as a server default", c)
		}
	}
}

func TestTheEnginePoliciesValidate(t *testing.T) {
	on := true
	good := []*Config{
		{KV: &KV{RollingWindow: "on", ResidencyMode: ResidencyWindow}},
		{KV: &KV{RollingWindow: "512"}},
		{KV: &KV{RollingWindow: "off", ResidencyMode: ResidencyHead}},
		{Draft: &Draft{AutoMTPPolicy: "measured"}},
		{Fit: &Fit{Enabled: &on, VRAMTargetMiB: 22000}},
		{Engine: EngineLlamaCpp, Fit: &Fit{Enabled: &on}},
	}
	for _, c := range good {
		c.Version = c.requiredVersion()
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
		if c.Version != 6 {
			t.Errorf("%+v: version %d, want 6", c, c.Version)
		}
	}
	bad := []*Config{
		{KV: &KV{RollingWindow: "on", ResidencyMode: ResidencyHead}},
		{KV: &KV{RollingWindow: "huge"}},
		{KV: &KV{RollingWindow: "0"}},
		{Engine: EngineLlamaCpp, KV: &KV{RollingWindow: "on"}},
		{Draft: &Draft{AutoMTPPolicy: "sometimes"}},
		{Engine: EngineLlamaCpp, Draft: &Draft{AutoMTPPolicy: "off"}},
		{Engine: EngineLlamaCpp, Fit: &Fit{VRAMTargetMiB: 1000}},
	}
	for _, c := range bad {
		c.Version = c.requiredVersion()
		if c.Validate() == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestTheServerMayDefaultTheEnginePolicies(t *testing.T) {
	on := true
	own := &Config{KV: &KV{ResidencyMode: ResidencyHead}}
	def := &Config{Fit: &Fit{Enabled: &on}, KV: &KV{RollingWindow: "on"}}
	got, skipped := own.WithDefaults(def)
	// The model's head residency cannot take the server's rolling window, so
	// the kv default is left out; fit still applies.
	if len(skipped) != 1 || got.KV.RollingWindow != "" || got.Fit == nil {
		t.Fatalf("got %+v skipped %v", got, skipped)
	}
}
