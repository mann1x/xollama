package xollama

import "testing"

func TestTuningRaisesTheSchemaAndNeedsOpencoti(t *testing.T) {
	c := &Config{Tuning: &Tuning{FAQuantPrompt: "in-place"}}
	if got := c.requiredVersion(); got != 10 {
		t.Errorf("version %d, want 10", got)
	}
	c.Version = 10
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	if c.IsZero() {
		t.Error("IsZero")
	}
	c.Engine = EngineLlamaCpp
	if err := c.Validate(); err == nil {
		t.Error("accepted on stock llama.cpp")
	}
}

func TestTuningRefusesWhatTheEngineWould(t *testing.T) {
	below := -2
	for name, tu := range map[string]*Tuning{
		"state":   {RecurrentStateType: "f8"},
		"prompt":  {FAQuantPrompt: "inplace"},
		"error":   {AdmissionOnError: "allow"},
		"kvarn7":  {DraftKVarN: "kvarn7"},
		"mixed":   {DraftTypeK: "kvarn3", DraftTypeV: "kvarn4"},
		"oneside": {DraftTypeK: "kvarn3"},
		"shrink":  {SlotShrinkIdleMs: 500},
		"ttl":     {ElasticAllocTTL: &below},
		"share":   {GPUShareWeight: -1},
	} {
		if err := (&Config{Version: 10, Tuning: tu}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
