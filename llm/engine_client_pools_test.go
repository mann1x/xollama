package llm

import (
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// A client's pool seats reach the engine flag -- with xollama's own pooling
// off -- and never its own registry, which only counts pools it creates.
func TestClientPoolSeatsReachTheEngineOnly(t *testing.T) {
	t.Setenv("XOLLAMA_POLYKV_CLIENT_POOLS", "")
	off := false
	cfg := LlamaServerConfig{Xollama: &xollama.Config{Session: &xollama.Session{Pool: &off, ClientPools: 3}}}
	if got := enginePoolSeats(cfg, false); got != 3 {
		t.Fatalf("seats = %d, want the client's 3", got)
	}
	if got := effectivePoolCount(cfg, false); got != 0 {
		t.Fatalf("xollama's registry holds %d; the client's seats are not its own", got)
	}
	if got := enginePoolSeats(cfg, true); got != 0 {
		t.Fatalf("a multimodal load has %d seats", got)
	}
	cfg.CouncilPools = 6
	if got := enginePoolSeats(cfg, false); got != 9 {
		t.Fatalf("beside a council: %d, want 9", got)
	}

	// The environment answers for a model that names none.
	t.Setenv("XOLLAMA_POLYKV_CLIENT_POOLS", "2")
	if got := enginePoolSeats(LlamaServerConfig{}, false); got != 2 {
		t.Fatalf("from the environment: %d", got)
	}
	if got := enginePoolSeats(cfg, false); got != 9 {
		t.Fatalf("the model's own number wins: %d", got)
	}
}
