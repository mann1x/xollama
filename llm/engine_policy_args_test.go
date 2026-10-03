package llm

import (
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestNoStatedPolicyAddsNoArgument(t *testing.T) {
	for _, x := range []*xollama.Config{nil, {}, {KV: &xollama.KV{K: "q8_0", V: "q8_0"}}} {
		if got := appendEnginePolicyArgs([]string{"-m", "x"}, x, true); len(got) != 2 {
			t.Fatalf("%+v added %v", x, got[2:])
		}
	}
}

func TestStatedPoliciesReachTheEngine(t *testing.T) {
	off := false
	x := &xollama.Config{
		Fit:   &xollama.Fit{Enabled: &off, VRAMTargetMiB: 20000},
		KV:    &xollama.KV{RollingWindow: "256"},
		Draft: &xollama.Draft{AutoMTPPolicy: "always"},
	}
	got := strings.Join(appendEnginePolicyArgs(nil, x, true), " ")
	if got != "--fit off --vram-target 20000 --kv-rolling-window 256 --auto-mtp-policy always" {
		t.Fatalf("opencoti: %q", got)
	}
	// Stock llama.cpp knows --fit and nothing else here.
	if got := appendEnginePolicyArgs(nil, x, false); !slices.Equal(got, []string{"--fit", "off"}) {
		t.Fatalf("stock: %v", got)
	}
}
