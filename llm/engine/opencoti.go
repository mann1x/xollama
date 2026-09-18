package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ollama/ollama/envconfig"
)

// EnvPath points at one opencoti-llamafile artifact and skips discovery.
const EnvPath = "XOLLAMA_ENGINE_PATH"

// artifactPrefix is how a published artifact is named:
//
//	opencoti-llamafile-<version>-<tag>-<arch>.llamafile[.exe]
const artifactPrefix = "opencoti-llamafile-"

// ErrNotFound means no artifact is installed. It is never fatal: the caller
// falls back to the stock llama-server.
var ErrNotFound = errors.New("no opencoti-llamafile artifact found")

// DefaultDirs lists where an artifact is looked for, in order. libOllamaPath
// is ollama's own library directory (ml.LibOllamaPath); home is the user's
// home directory. Either may be empty.
func DefaultDirs(libOllamaPath, home string) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".ollama", "engines"))
	}
	if libOllamaPath != "" {
		dirs = append(dirs, filepath.Join(libOllamaPath, "engines"), libOllamaPath)
	}
	return dirs
}

// Find returns the artifact to run.
//
// explicit is the value of XOLLAMA_ENGINE_PATH: when set it is used as given
// and a missing file is an error rather than a reason to keep looking, because
// silently ignoring an explicit path is how you end up debugging the wrong
// binary.
//
// Otherwise the newest artifact across dirs wins. Newest by modification time,
// not by version string: "0.10.5-c7" does not order under any stock comparison
// and a wrong guess would silently pick an older engine.
func Find(explicit string, dirs []string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("%s=%q: %w", EnvPath, explicit, err)
		}
		return explicit, nil
	}

	type candidate struct {
		path string
		mod  int64
	}
	var found []candidate
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isArtifact(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			found = append(found, candidate{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("%w in %v", ErrNotFound, dirs)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].mod != found[j].mod {
			return found[i].mod > found[j].mod
		}
		return found[i].path < found[j].path
	})
	return found[0].path, nil
}

func isArtifact(name string) bool {
	if len(name) <= len(artifactPrefix) || name[:len(artifactPrefix)] != artifactPrefix {
		return false
	}
	switch filepath.Ext(name) {
	case ".llamafile", ".exe":
		return true
	}
	return false
}

// Command turns ollama's llama-server argv into the command line that runs the
// same load on an opencoti-llamafile artifact. params is passed through
// untouched — every flag ollama builds is accepted by the artifact as-is
// (measured; see docs/evaluations/phase0-engine-compat.md) — with two
// additions:
//
//   - --server, because an artifact launched without it is a chat CLI, not a
//     server.
//   - --gpu, so the engine uses the same backend family ollama picked the
//     devices from rather than re-probing and possibly choosing another.
//
// On anything but Windows the artifact is launched through sh. It is a
// Cosmopolitan APE, and a kernel without binfmt_misc APE registration cannot
// exec it directly.
// logVerbosity is the threshold opencoti-llamafile needs before it prints the
// allocation lines ollama's memory accounting scrapes.
//
// llamafile filters LLAMA_LOG_INFO at default verbosity, and the
// "<component>: <device> <kind> buffer size = N MiB" lines are INFO -- so
// without this the engine starts fine and memoryParsingWriter sees NOTHING,
// leaving memTotal and memGPU at zero and the scheduler blind. Upstream
// llama-server prints them at default verbosity, so the stock path never
// needed a flag and the gap is invisible until measured.
//
// Measured on solidPC with the exact argv this function builds:
//
//	threshold   boot lines   buffer lines   lines per request
//	(none)            19            0             -
//	3                 18            0             8
//	4                247           12            39
//	5               1902           20           536
//	-v              1902           20           631
//
// 4 is tempting and wrong. It drops "load_tensors: CUDA_Host model buffer
// size" entirely. That line reads 0.00 MiB on a full offload, which is why it
// looks free to lose, but on a partial offload it carries real weight bytes
// and its absence silently understates memTotal -- the same class of bug as
// the stale-buffer one memoryParsingWriter exists to prevent.
//
// The ~500 lines per request are the engine's logging design, not something
// this adapter can tune around: no threshold prints the boot allocation
// without also enabling per-token logging. Raised with the opencoti session.
const logVerbosity = "5"

func Command(artifact string, params []string, devices []Device, goos string) (string, []string) {
	args := make([]string, 0, len(params)+5)
	args = append(args, "--server")
	args = append(args, withoutLogVerbosity(params)...)
	// After the stock params, not before them. llama.cpp's parser takes the
	// last occurrence of a flag, and ollama passes --log-verbosity 4 of its
	// own; prepending ours left it inert and the scheduler planning against
	// the buffer-size lines that level 4 filters out.
	args = append(args, "--log-verbosity", logVerbosity)
	if gpu := gpuFlag(devices); gpu != "" {
		args = append(args, "--gpu", gpu)
	}

	if goos == "windows" {
		return artifact, args
	}
	return "sh", append([]string{artifact}, args...)
}

// withoutLogVerbosity drops any --log-verbosity and its value, so the argv
// carries exactly one rather than relying on last-wins to be read correctly by
// whoever next looks at the command line.
func withoutLogVerbosity(params []string) []string {
	out := make([]string, 0, len(params))
	for i := 0; i < len(params); i++ {
		if params[i] == "--log-verbosity" {
			i++ // also drop its value
			continue
		}
		if strings.HasPrefix(params[i], "--log-verbosity=") {
			continue
		}
		out = append(out, params[i])
	}
	return out
}

// gpuFlag maps the devices ollama selected onto the artifact's --gpu selector.
// An empty result means "say nothing and let it probe".
func gpuFlag(devices []Device) string {
	sawCPU := false
	for _, d := range devices {
		switch d.Backend {
		case BackendCUDA:
			return "nvidia"
		case BackendVulkan:
			return "vulkan"
		case BackendCPU:
			sawCPU = true
		}
	}
	if sawCPU || len(devices) == 0 {
		return "disable"
	}
	return ""
}

// Launch resolves the engine for one load and returns the command to run.
//
// It never fails: anything that goes wrong — no artifact installed, a bad
// XOLLAMA_ENGINE_PATH — falls back to the stock llama-server ollama already
// found, with the reason logged once. An engine swap is not worth a failed
// load.
func Launch(stockExe string, params []string, devices []Device, libOllamaPath string) (string, []string) {
	decision := Resolve(Host(), devices, envconfig.Var(EnvSelector))
	if decision.Kind != KindOpencoti {
		slog.Debug("using stock llama-server", "reason", decision.Reason)
		return stockExe, params
	}

	home, _ := os.UserHomeDir()
	artifact, err := Find(envconfig.Var(EnvPath), DefaultDirs(libOllamaPath, home))
	if err != nil {
		slog.Info("falling back to stock llama-server", "reason", decision.Reason, "error", err)
		return stockExe, params
	}

	name, args := Command(artifact, params, devices, runtime.GOOS)
	slog.Info("using opencoti-llamafile", "artifact", artifact, "reason", decision.Reason)
	return name, args
}
