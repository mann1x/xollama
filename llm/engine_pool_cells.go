package llm

import (
	"log/slog"
	"slices"
	"strconv"

	"github.com/ollama/ollama/ml"
)

// xollama-hook: pool-cells
//
// The size of the shared KV pool, stated apart from num_ctx (kv.pool_cells).
//
// Upstream starts the engine with -c num_ctx x num_parallel. That is right for
// stock llama.cpp, which splits -c evenly between its slots, and it is what the
// scheduler predicts memory from. On opencoti with one shared pool
// (--kv-unified) the product ties two different things together: num_ctx is
// the most one conversation may hold, the pool is what every slot and PolyKV
// session draws on. Tied, the pool can only be a multiple of num_ctx: on a
// 24 GB 3090 a kvarn3 omnimerge IQ2_M holds 655360 cells resident, but with
// num_ctx 262144 it could only be given 524288 or a spilling 786432
// (2026-10-24).
//
// The launch and the prediction both read poolCells, so the scheduler still
// predicts the process it starts: the reason -c is refused in
// XOLLAMA_ENGINE_ARGS holds.

// statedPoolCells is the model's kv.pool_cells, 0 when unstated.
func statedPoolCells(cfg LlamaServerConfig) int {
	if cfg.Xollama == nil || cfg.Xollama.KV == nil {
		return 0
	}
	return max(cfg.Xollama.KV.PoolCells, 0)
}

// poolCells is the -c an opencoti load gets: the stated pool, never below one
// num_ctx (a pool smaller than one conversation's window could not hold the
// prompt ollama allows it), else num_ctx x num_parallel.
func poolCells(cfg LlamaServerConfig, numCtx, numParallel int) int {
	if n := statedPoolCells(cfg); n > 0 {
		return max(n, numCtx)
	}
	return numCtx * max(numParallel, 1)
}

// PoolCellsFor is the pool the scheduler predicts memory for. predicted is
// upstream's figure (num_ctx x num_parallel, num_ctx clamped as the launch will
// clamp it); a load stock llama.cpp may serve keeps it, to the byte.
func PoolCellsFor(cfg LlamaServerConfig, gpus []ml.DeviceInfo, numCtx, predicted int) int {
	if statedPoolCells(cfg) == 0 || !wouldUseOpencoti(cfg, gpus) {
		return predicted
	}
	return poolCells(cfg, numCtx, 1)
}

// withPoolCells rewrites -c to the stated pool once the engine is known, and
// makes sure the pool is shared: without --kv-unified the engine divides -c
// between its slots, and each would get a share no request was sized for.
// Unstated, or on stock llama.cpp, the argv is returned untouched.
func withPoolCells(args []string, cfg LlamaServerConfig, numCtx, numParallel int, usedOpencoti bool) []string {
	stated := statedPoolCells(cfg)
	if !usedOpencoti || stated == 0 {
		return args
	}
	i := slices.Index(args, "-c")
	if i < 0 || i+1 >= len(args) {
		return args
	}
	pool := poolCells(cfg, numCtx, numParallel)
	if pool > stated {
		slog.Warn("kv.pool_cells is smaller than one conversation's window; the pool is num_ctx instead",
			"pool_cells", stated, "num_ctx", numCtx)
	}
	out := slices.Clone(args)
	out[i+1] = strconv.Itoa(pool)
	if !slices.Contains(out, "--kv-unified") {
		out = append(out, "--kv-unified")
	}
	slog.Info("KV pool sized by kv.pool_cells", "pool_cells", pool, "num_ctx", numCtx, "parallel", numParallel,
		"upstream_pool", numCtx*max(numParallel, 1))
	return out
}
