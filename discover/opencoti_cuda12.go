package discover

// xollama: CUDA devices listed by the library that serves them (the V100
// tester, 2026-09-27). Additive; used by overlayOpencotiDevices and the
// free-memory refresh.
//
// Discovery replaces llama.cpp's CUDA devices with the engine's own list. The
// engine holds two CUDA libraries beside itself and one process loads one: by
// its own pick that is CUDA 13 unless every NVIDIA card is older than compute
// 7.5, and CUDA 13 has no Volta and needs driver 580. So on a host with a V100
// beside a newer card, or a V100 on a 570-series driver, the listing by that
// pick has no V100, the card is dropped as "a device the engine that serves it
// does not list", and the load never reaches the CUDA 12 library. So the CUDA
// listing is also asked of the CUDA 12 library, when the pin carries one, and
// the longer list is kept.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
)

// opencotiHasCUDA12 reports whether the CUDA 12 library the pin names for this
// host is staged beside artifact. An operator's XOLLAMA_ENGINE_PATH names an
// engine that makes its own pick, so there is no second listing to ask for.
var opencotiHasCUDA12 = func(artifact string) bool {
	if envconfig.Var(engine.EnvPath) != "" {
		return false
	}
	pin, err := engine.DefaultPin()
	if err != nil {
		return false
	}
	arch, err := engine.PackageArch(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false
	}
	lib, ok := pin.CUDA12DSO(arch)
	if !ok {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(artifact), lib.StagedName()))
	return err == nil
}

// opencotiListLegacyCUDA is the CUDA listing with the engine told to load its
// CUDA 12 library. A variable so tests can stand in for the artifact.
var opencotiListLegacyCUDA = func(ctx context.Context, artifact string) (string, error) {
	return listDevices(ctx, artifact, engine.BackendCUDA, engine.EnvCUDALegacy+"=1")
}

// cudaLister remembers which library listed the CUDA devices, so a refresh
// asks for that one alone instead of booting the engine twice before every
// load.
var cudaLister struct {
	sync.Mutex
	known  bool
	legacy bool
}

// opencotiListing lists one backend's devices through artifact, and for CUDA
// also through its CUDA 12 library: the longer list wins, the engine's own
// pick on a tie.
func opencotiListing(ctx context.Context, artifact string, b engine.Backend) ([]opencotiDevice, error) {
	if b != engine.BackendCUDA {
		output, err := opencotiListDevices(ctx, artifact, b)
		return parseOpencotiDevices(output, string(b)), err
	}
	cudaLister.Lock()
	known, legacy := cudaLister.known, cudaLister.legacy
	cudaLister.Unlock()
	if known {
		list := opencotiListDevices
		if legacy {
			list = func(ctx context.Context, artifact string, _ engine.Backend) (string, error) {
				return opencotiListLegacyCUDA(ctx, artifact)
			}
		}
		output, err := list(ctx, artifact, b)
		return parseOpencotiDevices(output, string(b)), err
	}

	output, err := opencotiListDevices(ctx, artifact, b)
	listed := parseOpencotiDevices(output, string(b))
	if opencotiHasCUDA12(artifact) {
		altOut, altErr := opencotiListLegacyCUDA(ctx, artifact)
		if more := parseOpencotiDevices(altOut, string(b)); len(more) > len(listed) {
			slog.Info("opencoti: the CUDA 12 library lists devices the engine's own pick does not",
				"picked", len(listed), "cuda12", len(more))
			listed, err, legacy = more, altErr, true
		}
	}
	if len(listed) > 0 {
		cudaLister.Lock()
		cudaLister.known, cudaLister.legacy = true, legacy
		cudaLister.Unlock()
	}
	return listed, err
}
