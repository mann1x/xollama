package llm

import (
	"testing"

	"github.com/ollama/ollama/fs/gguf"
)

// The rule `show` prints must be the rule the launch runs, so it is tested as
// one function rather than twice in two shapes.
func TestTheSpecTypeShowReportsIsTheOneTheLaunchWouldPass(t *testing.T) {
	assistant := &DrafterMetadata{Architecture: "gemma4-assistant", RequiresTargetArch: "gemma4"}
	plain := &DrafterMetadata{Architecture: "qwen35"}

	for _, tt := range []struct {
		name       string
		attached   *DrafterMetadata
		targetArch string
		builtIn    bool
		pin        string
		want       string
		wantErr    bool
	}{
		{name: "no drafter at all states nothing", want: ""},
		{name: "no drafter ignores a pin", pin: draftTypeAssistant, want: ""},
		{name: "attached assistant head", attached: assistant, targetArch: "gemma4", want: draftTypeAssistant},
		{name: "attached plain drafter", attached: plain, targetArch: "qwen35", want: draftTypeMTP},
		{name: "built-in head", builtIn: true, targetArch: "qwen35", want: draftTypeMTP},
		{name: "built-in head, pinned", builtIn: true, targetArch: "qwen35", pin: draftTypeAssistant, want: draftTypeAssistant},
		{name: "attached head, pinned", attached: assistant, targetArch: "gemma4", pin: draftTypeMTP, want: draftTypeMTP},
		{name: "head built for another target", attached: assistant, targetArch: "qwen35", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SpecTypeForShow(tt.attached, tt.targetArch, tt.builtIn, tt.pin)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SpecTypeForShow = %q, want an error naming the target mismatch", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("SpecTypeForShow = %q, want %q", got, tt.want)
			}
		})
	}
}

// Both spellings of a built-in head, because the count arrived after the
// tensors did and qwen35 predates it.
func TestABuiltInHeadIsRecognisedByEitherSpelling(t *testing.T) {
	mtp := []gguf.TensorInfo{{Name: "mtp.0.weight"}}

	for _, tt := range []struct {
		name    string
		arch    string
		nextn   uint64
		tensors []gguf.TensorInfo
		want    bool
	}{
		{name: "nextn count", arch: "gemma4", nextn: 4, want: true},
		{name: "qwen35 mtp tensors", arch: "qwen35", tensors: mtp, want: true},
		{name: "qwen35moe mtp tensors", arch: "qwen35moe", tensors: mtp, want: true},
		{name: "mtp tensors on another arch are not a head", arch: "gemma4", tensors: mtp, want: false},
		{name: "neither", arch: "gemma4", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuiltInDrafter(tt.arch, tt.nextn, tt.tensors); got != tt.want {
				t.Fatalf("BuiltInDrafter(%q, %d, %d tensors) = %v, want %v", tt.arch, tt.nextn, len(tt.tensors), got, tt.want)
			}
		})
	}
}
