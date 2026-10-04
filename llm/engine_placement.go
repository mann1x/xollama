package llm

// NeedsOpencoti is why this model cannot be served by stock llama.cpp, or ""
// when it can: a KV cache type the model itself states (kv.k / kv.v) that stock
// llama.cpp does not know. A server-wide XOLLAMA_K/V_CACHE_TYPE does not count
// -- on stock it falls back to the legacy type (startLlamaServer), so it never
// refuses a load. The scheduler reads this before it picks a backend
// (server/placement_opencoti.go): on a host with a second GPU only llama.cpp
// serves, the backend with more free memory could otherwise be the one the
// launch then refuses.
func NeedsOpencoti(cfg LlamaServerConfig) string {
	return resolveKVCacheTypesOn(cfg, "", true).requiresEngineExtension()
}
