package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestEveryTuningSettingReachesTheEngine(t *testing.T) {
	ttl := 0
	x := &xollama.Config{Tuning: &xollama.Tuning{
		RecurrentStateType: "f32", FAQuantPrompt: "in-place", AdmissionOnError: "deny",
		DraftKVarN: "inherit", DraftTypeK: "q8_0", DraftTypeV: "q4_0", TailTypeK: "q8_0", TailTypeV: "q4_0",
		StageWindow: 4, SlotInitialCtx: 1024, SlotShrinkIdleMs: 500, ElasticAllocTTL: &ttl, GPUShareWeight: 1.5,
	}}
	got := strings.Join(appendEnginePolicyArgs(nil, x, true), " ")
	for _, want := range []string{
		"--recurrent-state-type f32", "--fa-quant-prompt in-place", "--polykv-adm-on-error deny",
		"--spec-draft-kvarn inherit", "--cache-type-k-draft q8_0", "--cache-type-v-draft q4_0",
		"--cache-type-k-tail q8_0", "--cache-type-v-tail q4_0", "--kvarn-stage-window 4",
		"--slot-initial-ctx 1024", "--slot-shrink-idle-ms 500", "--elastic-alloc-ttl 0", "--gpu-share-weight 1.5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	if got := appendEnginePolicyArgs(nil, x, false); len(got) != 0 {
		t.Errorf("stock llama.cpp was given %v", got)
	}
	if got := appendEnginePolicyArgs(nil, &xollama.Config{Tuning: &xollama.Tuning{}}, true); len(got) != 0 {
		t.Errorf("an empty section added %v", got)
	}
}

func TestAdmissionHeadersReachTheCollectorOnce(t *testing.T) {
	ctx, w := WithContextWindow(context.Background())
	report := func(name, v string) {
		h := http.Header{}
		if name != "" {
			h.Set(name, v)
		}
		res := &http.Response{StatusCode: http.StatusOK, Header: h, Body: http.NoBody}
		defer res.Body.Close()
		reportEngineWindow(ctx, res)
	}
	report("", "")
	if w.Admission() != nil {
		t.Fatal("a stock reply reported admission")
	}
	report("X-KV-Pool-Saturated", "3")
	report("X-KV-Pool-Saturated", "9")
	if got := w.Admission().Get("X-KV-Pool-Saturated"); got != "3" {
		t.Errorf("got %q, want the first report", got)
	}
}
