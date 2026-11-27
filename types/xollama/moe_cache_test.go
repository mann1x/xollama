package xollama_test

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestTheMoECacheSettingsAreChecked(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	b := func(v bool) *bool { return &v }
	for _, tc := range []struct {
		name string
		fit  xollama.Fit
		want string // "" = accepted, else a fragment of the refusal
	}{
		{"auto", xollama.Fit{MoECache: "auto"}, ""},
		{"off", xollama.Fit{MoECache: "off"}, ""},
		{"a size", xollama.Fit{MoECache: "4096", MoECacheCPUShare: f(0.5), MoECacheWarmStart: b(false)}, ""},
		{"a word", xollama.Fit{MoECache: "big"}, "fit.moe_cache"},
		{"zero", xollama.Fit{MoECache: "0"}, "fit.moe_cache"},
		{"a share past 1", xollama.Fit{MoECacheCPUShare: f(1.5)}, "cpu_share"},
		{"a share with off", xollama.Fit{MoECache: "off", MoECacheCPUShare: f(0.2)}, "no cache to share"},
		{"a warm start with off", xollama.Fit{MoECache: "off", MoECacheWarmStart: b(true)}, "no cache to warm"},
		{"auto with the fit off", xollama.Fit{MoECache: "auto", Enabled: b(false)}, "decided by the fit"},
		{"auto with another strategy", xollama.Fit{MoECache: "auto", MoEStrategy: "kv-gpu"}, "only the auto strategy"},
		{"a size with the fit off", xollama.Fit{MoECache: "2048", Enabled: b(false)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fit := tc.fit
			err := (&xollama.Config{Version: 10, Fit: &fit}).Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
	pinned := &xollama.Config{Version: 1, Engine: xollama.EngineLlamaCpp, Fit: &xollama.Fit{MoECache: "off"}}
	if err := pinned.Validate(); err == nil || !strings.Contains(err.Error(), "opencoti") {
		t.Fatalf("a model pinned to llamacpp kept a MoE cache setting: %v", err)
	}
}

// Every older build ships an engine with no MoE cache, so the settings raise
// no schema version: an older build drops them and runs the model as it
// would anyway.
func TestTheMoECacheRaisesNoSchemaVersion(t *testing.T) {
	c := &xollama.Config{Version: 1, Fit: &xollama.Fit{MoECache: "off"}}
	if c.IsZero() {
		t.Fatal("a config stating only the MoE cache reads as empty")
	}
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := xollama.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != xollama.SchemaVersionBase || parsed.Fit == nil || parsed.Fit.MoECache != "off" {
		t.Fatalf("version %d fit %+v", parsed.Version, parsed.Fit)
	}
}
