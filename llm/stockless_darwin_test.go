//go:build darwin

package llm

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm/engine"
)

// On macOS a load that does not get opencoti is refused: there is no stock
// llama-server to run it, and none is looked for.
func TestAMacOSLoadThatDoesNotGetOpencotiIsRefused(t *testing.T) {
	t.Setenv(engine.EnvSelector, "llamacpp")
	launch := llamaServerLaunchConfig{modelPath: "/nonexistent.gguf", opts: api.DefaultOptions(), numParallel: 1}
	launch.opts.NumCtx = 2048
	_, _, used, err := startLlamaServer(launch, nil)
	if used || err == nil || !strings.Contains(err.Error(), "ships no stock llama-server") {
		t.Fatalf("want the stockless refusal, got used=%v err=%v", used, err)
	}
}
