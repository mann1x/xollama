package discover

// xollama: CUDA devices listed by the payload that serves them (the V100
// tester, 2026-09-27). Additive; used by overlayOpencotiDevices and the
// free-memory refresh.
//
// Discovery replaces llama.cpp's CUDA devices with the engine's own list, and
// the engine it asked was the main one, whose CUDA 13 payload needs driver
// 580 and has no Volta. On a V100 with a 570-series driver -- exactly what
// the CUDA 12 payload in engines/cuda_v12 is for -- that engine lists nothing,
// the V100 is dropped as "a device the engine that serves it does not list",
// and the load never reaches the CUDA 12 payload. So the CUDA listing also
// asks the CUDA 12 engine, when there is one, and keeps the longer list.

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// opencotiCUDA12Artifact locates the engine beside the CUDA 12 payload, as a
// launch that needs it does. An operator's XOLLAMA_ENGINE_PATH names one
// engine for everything, so there is no second one to ask.
var opencotiCUDA12Artifact = func() (string, error) {
	if envconfig.Var(engine.EnvPath) != "" {
		return "", os.ErrNotExist
	}
	home, _ := os.UserHomeDir()
	return engine.Find("", engine.CUDA12Dirs(ml.LibOllamaPath, home))
}

// cudaLister remembers which engine listed the CUDA devices, so a refresh
// asks that one alone instead of booting both before every load.
var cudaLister struct {
	sync.Mutex
	artifact string
}

// opencotiListing lists one backend's devices through artifact, and for CUDA
// also through the CUDA 12 engine: the longer list wins, the main engine on a
// tie.
func opencotiListing(ctx context.Context, artifact string, b engine.Backend) ([]opencotiDevice, error) {
	if b != engine.BackendCUDA {
		output, err := opencotiListDevices(ctx, artifact, b)
		return parseOpencotiDevices(output, string(b)), err
	}
	cudaLister.Lock()
	chosen := cudaLister.artifact
	cudaLister.Unlock()
	if chosen != "" {
		output, err := opencotiListDevices(ctx, chosen, b)
		return parseOpencotiDevices(output, string(b)), err
	}

	output, err := opencotiListDevices(ctx, artifact, b)
	listed, from := parseOpencotiDevices(output, string(b)), artifact
	if alt, aerr := opencotiCUDA12Artifact(); aerr == nil && alt != artifact {
		altOut, altErr := opencotiListDevices(ctx, alt, b)
		if more := parseOpencotiDevices(altOut, string(b)); len(more) > len(listed) {
			slog.Info("opencoti: the CUDA 12 payload lists devices the main engine does not",
				"main", len(listed), "cuda12", len(more))
			listed, err, from = more, altErr, alt
		}
	}
	if len(listed) > 0 {
		cudaLister.Lock()
		cudaLister.artifact = from
		cudaLister.Unlock()
	}
	return listed, err
}
