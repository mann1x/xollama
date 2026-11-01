package xollama

import "testing"

func TestTheC14PoliciesRaiseTheSchema(t *testing.T) {
	on := true
	for name, c := range map[string]*Config{
		"matvec":   {KV: &KV{VulkanMatvecTune: &on}},
		"moe":      {Fit: &Fit{MoEStrategy: "experts-gpu"}},
		"adaptive": {Draft: &Draft{AutoMTPPolicy: AutoMTPAdaptive}},
	} {
		if got := c.requiredVersion(); got != 10 {
			t.Errorf("%s: version %d, want 10", name, got)
		}
		c.Version = 10
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if c.IsZero() {
			t.Errorf("%s: IsZero", name)
		}
		c.Engine = EngineLlamaCpp
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted on stock llama.cpp", name)
		}
	}
}

func TestAnUnknownMoEStrategyIsRefused(t *testing.T) {
	c := &Config{Version: 10, Fit: &Fit{MoEStrategy: "experts"}}
	if err := c.Validate(); err == nil {
		t.Error("an unknown strategy was accepted")
	}
}

func TestTheAdmissionGateTakesOnlyTheEnginesValues(t *testing.T) {
	for _, v := range ValidAdmissionPoolless() {
		c := &Config{KV: &KV{AdmissionPoolless: v}}
		if got := c.requiredVersion(); got != 10 {
			t.Errorf("%s: version %d", v, got)
		}
		c.Version = 10
		if err := c.Validate(); err != nil {
			t.Error(err)
		}
		if c.IsZero() {
			t.Errorf("%s: IsZero", v)
		}
	}
	bad := &Config{Version: 10, KV: &KV{AdmissionPoolless: "auto"}}
	if err := bad.Validate(); err == nil {
		t.Error("auto is no value of the flag, and the engine would refuse the launch")
	}
}
