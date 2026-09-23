package llm

import "testing"

// TestRingShapeMatchesWhatTheEngineAccepts encodes a probe of the pinned
// artifact, not a reading of the vendor's flag table. Every row here was first
// run against opencoti-0.10.5-c7-2609200554001, and re-run unchanged against
// opencoti-0.10.5-c7-2609230556001 on 2026-09-23 when the pin moved there, as
//
//	<artifact> --server <flags> --model /nonexistent.gguf
//
// where reaching "failed to load model" means the command line was accepted
// and anything earlier means it was refused.
//
// The verdicts did not move across that pin. What changed is that the engine
// now names the rule in its own refusal -- "--cache-type-k/v-swa overrides
// require KVarN --cache-type-k" and "a plain SWA cache type requires both
// --cache-type-k-swa and ..." -- so the base rule the flag table never stated
// is finally stated somewhere. Refusing these shapes before launch is still
// worth doing: the engine states it by exiting during startup, which reaches
// an operator as a model that would not load.
func TestRingShapeMatchesWhatTheEngineAccepts(t *testing.T) {
	cases := []struct {
		name     string
		kv       kvCacheTypes
		accepted bool
	}{
		{"no ring at all is always fine", kvCacheTypes{K: "f16", V: "f16"}, true},
		{"the shape the flag table recommends", kvCacheTypes{K: "kvarn3", V: "kvarn3", KSWA: "q4_0", VSWA: "q4_0"}, true},
		{"a KVarN ring on a KVarN base", kvCacheTypes{K: "kvarn3", V: "kvarn3", KSWA: "kvarn4", VSWA: "kvarn4"}, true},

		// Measured refusals. The base rule is the one the flag table does not
		// state, and it is the one an operator is most likely to trip: f16 is
		// the default, so setting only the ring looks like it should work.
		{"an f16 base cannot carry a ring", kvCacheTypes{K: "f16", V: "f16", KSWA: "q4_0", VSWA: "q4_0"}, false},
		{"nor can a plain quantised base", kvCacheTypes{K: "q8_0", V: "q8_0", KSWA: "q4_0", VSWA: "q4_0"}, false},
		{"ring halves may not be mixed KVarN and plain", kvCacheTypes{K: "kvarn3", V: "kvarn3", KSWA: "q4_0", VSWA: "kvarn3"}, false},
		{"half a ring is not a ring", kvCacheTypes{K: "kvarn3", V: "kvarn3", KSWA: "q4_0"}, false},
		{"one KVarN half of the base is not enough", kvCacheTypes{K: "kvarn3", V: "f16", KSWA: "q4_0", VSWA: "q4_0"}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.kv.ringShapeError()
			if tt.accepted && got != "" {
				t.Fatalf("ringShapeError() = %q, want it accepted", got)
			}
			if !tt.accepted && got == "" {
				t.Fatal("ringShapeError() = \"\", want a refusal; the engine would exit during startup instead")
			}
		})
	}
}
