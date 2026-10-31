package llm

import (
	"strconv"

	"github.com/ollama/ollama/types/xollama"
)

// appendEngineTuningArgs adds the flags of a config's tuning section. Each
// exists on every engine since c13, so none needs a pin feature.
func appendEngineTuningArgs(args []string, t *xollama.Tuning) []string {
	if t.IsZero() {
		return args
	}
	text := func(flag, v string) {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	number := func(flag string, n int) {
		if n != 0 {
			args = append(args, flag, strconv.Itoa(n))
		}
	}
	text("--recurrent-state-type", t.RecurrentStateType)
	text("--fa-quant-prompt", t.FAQuantPrompt)
	text("--polykv-adm-on-error", t.AdmissionOnError)
	text("--spec-draft-kvarn", t.DraftKVarN)
	text("--cache-type-k-draft", t.DraftTypeK)
	text("--cache-type-v-draft", t.DraftTypeV)
	text("--cache-type-k-tail", t.TailTypeK)
	text("--cache-type-v-tail", t.TailTypeV)
	number("--kvarn-stage-window", t.StageWindow)
	number("--slot-initial-ctx", t.SlotInitialCtx)
	number("--slot-shrink-idle-ms", t.SlotShrinkIdleMs)
	if t.ElasticAllocTTL != nil {
		args = append(args, "--elastic-alloc-ttl", strconv.Itoa(*t.ElasticAllocTTL))
	}
	if t.GPUShareWeight > 0 {
		args = append(args, "--gpu-share-weight", strconv.FormatFloat(t.GPUShareWeight, 'f', -1, 64))
	}
	return args
}
