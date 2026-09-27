package llm

// xollama: a council's live slots -- plans/agentic-council-chat.md, measured
// 2026-09-27 (ab-3). Additive; read by the `council` hooks in
// llama_server.go and engine_estimate.go.
//
// A council fans its researchers (and critics) out as concurrent requests,
// each a worker session charged to the owner's window. Workers need no cells
// of their own, but each needs a SLOT: with -np 1 the engine defers the second
// worker until the first releases slot 0, and the elastic controller does not
// help, because it promotes a parked slot only after 3 s of saturation and
// only with 512 MiB of free VRAM, which a fitted load rarely has (opencoti
// mail #501, from the b145 log: "no slot is available, defer task"). ab-3's
// two researchers ran back to back for that reason.
//
// So a council launches with -np at its widest parallel step. Live slots cost
// nothing over the same --max-parallel (the engine sizes KV, output buffer and
// batch for the ceiling at boot), and -c stays num_ctx x the model's own live
// count: the workers are charged to the owner, so the pool does not grow.

import (
	"slices"
	"strconv"
)

// councilLive is the live slot count a launch starts with: the scheduler's,
// raised to the council's width where opencoti serves a council model. It is
// never raised on stock llama.cpp, for a model held to one sequence, or for a
// model that splits its cells per slot (kv.unified false), where more slots
// would cut each one's share of -c.
func councilLive(cfg LlamaServerConfig, numParallel int, opencoti bool) int {
	if !opencoti || cfg.CouncilSlots <= numParallel || cfg.singleSequence(opencoti) {
		return numParallel
	}
	if x := cfg.Xollama; x != nil && x.KV != nil && x.KV.Unified != nil && !*x.KV.Unified {
		return numParallel
	}
	return cfg.CouncilSlots
}

// councilSlotArgs raises the argv's -np to live and makes the cells one
// shared pool, which several live slots over an unchanged -c require. The -c
// the scheduler wrote is left as it is.
func councilSlotArgs(args []string, live, numParallel int) []string {
	if live <= numParallel {
		return args
	}
	if i := slices.Index(args, "-np"); i >= 0 && i+1 < len(args) {
		args[i+1] = strconv.Itoa(live)
	}
	if !slices.Contains(args, "--kv-unified") {
		args = append(args, "--kv-unified")
	}
	return args
}
