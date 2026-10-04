package llm

// xollama: the engine-lifetime hook's body. Additive.
//
// An opencoti engine is tied to the server that started it
// (engine.BindLifetime; a job object on Windows, nothing elsewhere), so a
// server that is killed or crashes leaves no engine behind with a model loaded.
// Stock llama.cpp is started as upstream starts it.

import (
	"log/slog"
	"os/exec"

	"github.com/ollama/ollama/llm/engine"
)

// bindEngineLifetime never fails a load: an engine that could not be bound is
// said at Warn, because that engine is one a dead server would leave running.
func bindEngineLifetime(cmd *exec.Cmd) {
	if err := engine.BindLifetime(cmd); err != nil {
		slog.Warn("opencoti: the engine is not tied to this server and would outlive it if the server is killed", "pid", cmd.Process.Pid, "error", err)
	}
}
