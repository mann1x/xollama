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

func withPolicies(t *testing.T, why string) {
	t.Helper()
	old := policyUnavailable
	policyUnavailable = func(string, string) string { return why }
	t.Cleanup(func() { policyUnavailable = old })
}

func TestTheC14PoliciesReachTheEngine(t *testing.T) {
	on := true
	x := &xollama.Config{
		KV:    &xollama.KV{VulkanMatvecTune: &on},
		Fit:   &xollama.Fit{MoEStrategy: "kv-gpu"},
		Draft: &xollama.Draft{AutoMTPPolicy: xollama.AutoMTPAdaptive},
	}
	got := strings.Join(appendEnginePolicyArgs(nil, x, true), " ")
	for _, want := range []string{"--vulkan-matvec-tune on", "--moe-strategy kv-gpu", "--auto-mtp-policy adaptive"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	if got := appendEnginePolicyArgs(nil, x, false); len(got) != 0 {
		t.Errorf("stock llama.cpp was given %v", got)
	}
}

func TestAPolicyThePinDoesNotDeclareIsRefusedByName(t *testing.T) {
	withPolicies(t, "the engine does not declare it")
	on := false
	for _, x := range []*xollama.Config{
		{KV: &xollama.KV{VulkanMatvecTune: &on}},
		{Fit: &xollama.Fit{MoEStrategy: "auto"}},
		{Draft: &xollama.Draft{AutoMTPPolicy: xollama.AutoMTPAdaptive}},
	} {
		if err := enginePolicyRefusal(x, true); err == nil {
			t.Errorf("%+v was not refused", x)
		}
		if err := enginePolicyRefusal(x, false); err != nil {
			t.Errorf("stock llama.cpp refused: %v", err)
		}
	}
	if err := enginePolicyRefusal(&xollama.Config{Draft: &xollama.Draft{AutoMTPPolicy: "measured"}}, true); err != nil {
		t.Errorf("a policy every engine has was refused: %v", err)
	}
	withPolicies(t, "")
	if err := enginePolicyRefusal(&xollama.Config{Fit: &xollama.Fit{MoEStrategy: "auto"}}, true); err != nil {
		t.Errorf("a declared policy was refused: %v", err)
	}
}
