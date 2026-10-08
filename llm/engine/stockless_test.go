package engine

import "testing"

// macOS ships no stock llama-server (owner, 2026-10-05; done with c10); the
// other platforms keep it.
func TestOnlyMacOSShipsWithoutStockLlamaServer(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": false, "linux": true, "windows": true} {
		if got := StockShipped(goos); got != want {
			t.Errorf("StockShipped(%q) = %v, want %v", goos, got, want)
		}
	}
}
