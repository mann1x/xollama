package llm

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode"
)

// Operator argv passthrough -- gap G2 in docs/features/opencoti-config-gaps.md.
//
// Both engines carry flags with no LLAMA_ARG_* twin, so there is no way to
// reach them through the environment. `--override-kv` is the one that bites
// (common/arg.cpp declares it with no set_env), and the opencoti development
// builds add more with every cut. Without an escape hatch each of those needs a
// code change in this repo before anyone can even try it.
//
// The hatch is OPERATOR-SIDE ONLY: one environment variable, set by a person on
// their own machine. It is deliberately NOT a field in a model's xollama.json,
// and must never become one. A model is pulled from a registry and usually run
// unread; llama-server's command line can write files to caller-chosen paths
// and change what gets loaded, so a model able to append to that command line
// is a model able to act on the machine that ran it at pull-and-run time, with
// no prompt. Every model-side setting stays a named field with a checked value.
// See the decision note in .wolf/cerebrum.md (2026-09-19).
//
// Append-only, and appended last, after every argument xollama and ollama
// produce -- llama-server takes the final occurrence of a repeated flag, so
// last-wins puts the operator on the winning side without this code having to
// understand, reorder or remove anything already on the line.

// EngineArgsEnv is the variable the passthrough reads. Named here because the
// refusal messages quote it: "unknown argument" from a subprocess does not tell
// anyone which of several places set it.
const EngineArgsEnv = "XOLLAMA_ENGINE_ARGS"

// Why a flag is refused. Appending these would not break the engine -- it would
// break xollama's model OF the engine, which is worse, because nothing
// downstream can detect it.
const (
	// reasonEstimate: the scheduler decides whether a model fits, and how many
	// copies of the cache to pay for, from these numbers. PredictServerSlotVRAM
	// is handed the same context, batch and parallel figures that reach the
	// command line. Move one of them here and the engine allocates a size the
	// scheduler never predicted: the load is admitted against arithmetic that
	// is simply wrong, and the failure lands later, somewhere else, as an OOM
	// nobody can trace back to an environment variable.
	reasonEstimate = "the scheduler sizes VRAM and admits the load from this value, so overriding it here makes its memory estimate describe a process that does not exist"

	// reasonAddress: xollama starts this process, talks to it over a port it
	// chose, and reloads it when the model changes. Repointing any of that
	// leaves a runner ollama believes is serving one thing while it serves
	// another, or nothing.
	reasonAddress = "xollama starts, addresses and reuses the runner by this value, so overriding it here detaches the process from the server that owns it"

	// reasonLog: load progress, the port handshake and the engine-defect
	// matcher are all read out of the subprocess log.
	reasonLog = "xollama reads load progress and failures out of the subprocess log at the verbosity it set"
)

// guardedEngineFlags maps every spelling llama-server accepts to the reason it
// is refused. Both the short and long forms are listed because the engine
// accepts either, and a guard that only knew one spelling would be a guard in
// name only.
var guardedEngineFlags = map[string]string{
	"-c":             reasonEstimate,
	"--ctx-size":     reasonEstimate,
	"-np":            reasonEstimate,
	"--parallel":     reasonEstimate,
	"-ngl":           reasonEstimate,
	"--gpu-layers":   reasonEstimate,
	"--n-gpu-layers": reasonEstimate,
	"-b":             reasonEstimate,
	"--batch-size":   reasonEstimate,
	"-ub":            reasonEstimate,
	"--ubatch-size":  reasonEstimate,

	"-m":      reasonAddress,
	"--model": reasonAddress,
	"--host":  reasonAddress,
	"--port":  reasonAddress,

	"-lv":             reasonLog,
	"--log-verbosity": reasonLog,
	"--verbosity":     reasonLog,
	"--verbose":       reasonLog,
	"-v":              reasonLog,
	"--log-disable":   reasonLog,
	"--log-file":      reasonLog,
}

// appendEngineArgs appends the operator's extra arguments to a finished
// llama-server command line.
//
// An empty value returns args untouched, which is what keeps
// XOLLAMA_ENGINE=llamacpp byte-identical to upstream: with nothing set this
// function adds nothing, logs nothing and copies nothing.
//
// xollama-hook: engine-args
// engineArgsAdvisories names flags that the PINNED engine bytes are known to
// answer badly, in a way nothing downstream can notice.
//
// This is not the guarded list and must not become it. A guarded flag is one
// xollama derives itself, and passing it breaks the server's model of the
// process; these are flags the operator is entitled to pass, on bytes where the
// vendor has told us the result is wrong. The answer is a warning, not a
// refusal -- they may be measuring the defect deliberately, which is exactly
// what we do to them.
//
// It is also not llm/engine_defects.go. That table matches the engine's dying
// words against the artifact digest, so it can only catch a defect that says
// something. These say nothing at all: the run succeeds and the answer is
// quietly worse. The only moment anyone can be told is the moment the flag is
// accepted, here.
//
// Retire a row the day the pin moves to bytes that fix it, in the same commit
// as the pin -- same rule as a defect row, and for the same reason.
var engineArgsAdvisories = map[string]string{
	"--sparse-attn": "opencoti bug-3524: on the pinned dev build, --sparse-attn over a plain (non-KVarN) cache drops retrievable content -- a long-context lookup can silently miss what it was asked for. Nothing fails; the answer is just worse. Use a kvarnN cache type, or leave the flag off, until a build that fixes it is pinned.",
}

// warnAboutAdvisedFlags says so when the operator passes a flag the pinned bytes
// answer badly. Extracted so a test can drive it without a logger.
func advisoriesFor(extra []string) []string {
	var out []string
	for _, a := range extra {
		flag, _, _ := strings.Cut(a, "=")
		if why, ok := engineArgsAdvisories[flag]; ok {
			out = append(out, why)
		}
	}
	return out
}

func appendEngineArgs(args []string, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return args, nil
	}

	extra, err := splitEngineArgs(raw)
	if err != nil {
		return nil, fmt.Errorf("%s could not be parsed: %w", EngineArgsEnv, err)
	}
	if len(extra) == 0 {
		return args, nil
	}

	for _, tok := range extra {
		// --flag=value and --flag value are the same flag to the engine, so
		// the guard has to see past the '='.
		name, _, _ := strings.Cut(tok, "=")
		if !strings.HasPrefix(name, "-") || name == "-" {
			continue
		}
		if why, guarded := guardedEngineFlags[name]; guarded {
			return nil, fmt.Errorf("%s may not set %s: %s. Remove it from %s -- that setting has its own knob (see docs/xollama/engine-args.mdx)",
				EngineArgsEnv, name, why, EngineArgsEnv)
		}
	}

	// Named at INFO, not DEBUG. An engine started with arguments that are not
	// in this codebase is the first thing worth knowing when the run behaves
	// unlike everyone else's.
	slog.Info("appending operator-supplied engine arguments", "source", EngineArgsEnv, "args", extra, "count", len(extra))

	for _, why := range advisoriesFor(extra) {
		slog.Warn("a flag you passed is known to be answered badly by the pinned engine build",
			"source", EngineArgsEnv, "advisory", why)
	}

	return append(append([]string(nil), args...), extra...), nil
}

// splitEngineArgs splits a command-line-ish string into arguments.
//
// Whitespace separates; single and double quotes group and may contain spaces.
// A backslash is a LITERAL backslash, never an escape: the values that need
// this hatch are as likely to be Windows paths as anything else, and silently
// eating separators out of C:\models\foo would be a worse surprise than not
// supporting escapes. Quote the value instead.
func splitEngineArgs(s string) ([]string, error) {
	var (
		out    []string
		cur    strings.Builder
		quote  rune
		quoted bool
	)
	flush := func() {
		// quoted keeps a deliberate empty argument ("" or '') alive; without
		// it an operator could not pass an empty string at all.
		if cur.Len() > 0 || quoted {
			out = append(out, cur.String())
			cur.Reset()
			quoted = false
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			quoted = true
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	flush()
	return out, nil
}
