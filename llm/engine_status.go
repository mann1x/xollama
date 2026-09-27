package llm

import "strings"

// xollama-hook: engine-select — what the runner's stderr is allowed to call
// an error.
//
// The StatusWriter keeps every line that looks like an out-of-memory report as
// the runner's last error, and the scheduler expires every loaded model when an
// error carries one (expireRunnersForRuntimeOOM). ggml's
//
//	ggml_backend_sched_alloc_splits: failed to allocate graph, reserving (backend_ids_changed = 1)
//
// contains "failed to allocate", but it is not a failed allocation: the
// scheduler found its graph plan stale and re-reserves, which is routine.
// Stock llama-server logs it at debug level; opencoti prints it on ordinary
// loads and requests. So on opencoti the first engine error of any kind -- a
// 429 admission refusal, measured 2026-09-26 (bug-118) -- read as an
// out-of-memory, and a council turn that was only waiting for room expired
// the model.
//
// Only while opencoti serves: on llama.cpp the classification is upstream's.

// graphReserveLine is the one line that is not what it looks like.
const graphReserveLine = "failed to allocate graph, reserving"

func (w *StatusWriter) benign(line string) bool {
	return w.opencoti.Load() && strings.Contains(line, graphReserveLine)
}
