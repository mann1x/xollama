package api

// Placement puts a chat turn on the engine's KV cache (client_placement_v1),
// for a client that drives PolyKV itself, as it would against a bare opencoti:
// it builds its own pools through /api/engine and names one here.
//
// On a plain turn every field goes to the engine as the request's pool_id,
// num_ctx and num_ctx_min. On a council turn only PoolID is read: the council
// forks its conversation root from that pool when its prompt starts with the
// pool's tokens, and never releases it; the council sizes its own members.
// Every field is ignored on an engine without sessions, and on stock
// llama.cpp.
type Placement struct {
	// PoolID is the pool to attach to. The engine numbers its first pool 0,
	// so nil, not 0, means none.
	PoolID *int `json:"pool_id,omitempty"`
	// NumCtx and NumCtxMin are the session's window and the least it accepts.
	NumCtx    int `json:"num_ctx,omitempty"`
	NumCtxMin int `json:"num_ctx_min,omitempty"`
}
