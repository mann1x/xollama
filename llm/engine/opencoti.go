package engine

import (
	"errors"
	"fmt"
	"io/fs"
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

// EnvFallback opts in to retrying a failed opencoti load on stock
// llama-server. Off by default -- see FallbackOnLoadFailure.
const EnvFallback = "XOLLAMA_ENGINE_FALLBACK"

// artifactPrefix is how a published artifact is named.
//
// TWO shapes, because the two channels name them differently, and a finder
// that knows only one silently ships an engine nothing can load:
//
//	opencoti-llamafile-<version>-<tag>-<arch>.llamafile[.exe]   release channel
//	opencoti-<version>-<build>                                  dev channel, bare APE
//
// The dev artifact carries NO extension at all, which is what caught us: the
// build staged opencoti-0.10.5-c7-2609221142001 into the payload, Find skipped
// it for having no .llamafile suffix, and the server fell back to stock
// llama-server with nothing in the log about a pin having moved. The engine was
// in the package; it was simply invisible.
const artifactPrefix = "opencoti-"

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
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if !isArtifact(e.Name(), info.Mode()) {
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

// isArtifact reports whether a payload-directory entry is an engine to launch.
//
// The extension settles it for the release channel. It cannot settle it for the
// dev channel, and not merely because that name has no suffix: filepath.Ext of
// "opencoti-0.10.5-c7-2609221142001" is ".5-c7-2609221142001", because the
// version number contains dots. A test for an empty extension looks right and
// never fires.
//
// So two tests, in the order they can actually answer. A known extension
// settles it either way -- an engine, or one of the data files that travel
// beside one. What is left is a name the extension cannot classify, and there
// the EXECUTABLE BIT decides: the dev channel publishes its APE 0755 and its
// CUDA payload 0644 into the same directory, which is the difference that
// matters and the only one either file states about itself.
//
// Both signals have to agree before anything is launched. Neither alone is
// enough: a manifest named <artifact>.MANIFEST.json is executable on plenty of
// filesystems, and an artifact restored without its mode is still an artifact.
func isArtifact(name string, mode fs.FileMode) bool {
	if len(name) <= len(artifactPrefix) || name[:len(artifactPrefix)] != artifactPrefix {
		return false
	}
	switch filepath.Ext(name) {
	case ".llamafile", ".exe":
		return true
	case ".so", ".dll", ".dylib", ".json", ".txt", ".md", ".sha256", ".sig", ".zip", ".gz", ".xz", ".log":
		return false
	}
	return mode&0o111 != 0
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
	args = append(args, translateLoadMode(withoutLogVerbosity(params))...)
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

// translateLoadMode rewrites ollama's --load-mode, which this engine does not
// have, into the flag it does.
//
// ollama gained --load-mode as one flag covering how weights are read off disk:
// "none" means do not memory-map them, "dio" means bypass the page cache on an
// integrated GPU that would otherwise buffer them twice. The engine's llama.cpp
// predates that merge and still spells the first half --no-mmap. It has no
// equivalent of the second, which costs nothing here: "dio" is only ever chosen
// for an integrated CUDA or ROCm GPU on Linux, and this engine is not routed to
// on that hardware.
//
// This matters more than a missing flag usually would, because ollama disables
// mmap by default for a llama-server load. --load-mode none is therefore on
// essentially every argv, and an engine that rejects it can load nothing at
// all -- which is exactly what it did: "error: invalid argument: --load-mode",
// on every model, measured against opencoti-llamafile-0.10.5-c7.
//
// Translating rather than dropping is deliberate. Dropping would leave the
// weights memory-mapped after ollama had decided they should not be, and the
// scheduler's memory accounting is built on that decision.
func translateLoadMode(params []string) []string {
	out := make([]string, 0, len(params))
	for i := 0; i < len(params); {
		mode, consumed, ok := loadModeValue(params, i)
		if !ok {
			out = append(out, params[i])
			i++
			continue
		}
		if mode == "none" {
			out = append(out, "--no-mmap")
		}
		i += consumed
	}
	return out
}

// loadModeValue recognises --load-mode in either spelling, returning its value
// and how many arguments it occupies.
func loadModeValue(params []string, i int) (value string, consumed int, ok bool) {
	if params[i] == "--load-mode" {
		if i+1 < len(params) {
			return params[i+1], 2, true
		}
		return "", 1, true
	}
	if value, found := strings.CutPrefix(params[i], "--load-mode="); found {
		return value, 1, true
	}
	return "", 0, false
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
func Launch(stockExe string, params []string, devices []Device, libOllamaPath string) (string, []string, bool) {
	decision := Resolve(Host(), devices, envconfig.Var(EnvSelector))
	if decision.Kind != KindOpencoti {
		slog.Debug("using stock llama-server", "reason", decision.Reason)
		return stockExe, params, false
	}

	home, _ := os.UserHomeDir()
	artifact, err := Find(envconfig.Var(EnvPath), DefaultDirs(libOllamaPath, home))
	if err != nil {
		slog.Info("falling back to stock llama-server", "reason", decision.Reason, "error", err)
		return stockExe, params, false
	}

	name, args := Command(artifact, params, devices, runtime.GOOS)
	slog.Info("using opencoti-llamafile", "artifact", artifact, "reason", decision.Reason)
	return name, args, true
}

// FallbackOnLoadFailure reports whether a load that fails on opencoti should be
// retried on stock llama-server.
//
// Off by default, and deliberately so. Retrying silently would turn "this model
// does not work on the engine you selected" into "this model is quietly slower
// and has none of the engine's features", and an A/B against vanilla stops
// meaning anything. Opting in is a statement that availability matters more
// than knowing which engine answered.
func FallbackOnLoadFailure() bool {
	return envconfig.Bool(EnvFallback)()
}

// ArtifactOf returns the engine artifact a launch built by Command runs.
//
// Off Windows the artifact is an APE run through sh, so the program is "sh"
// and the artifact is its first argument. Anything that needs the artifact
// itself -- hashing it, finding its payload -- must ask here rather than use
// the program name, which on Linux names the shell.
func ArtifactOf(name string, args []string) string {
	if name == "sh" && len(args) > 0 {
		return args[0]
	}
	return name
}
