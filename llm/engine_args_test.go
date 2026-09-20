package llm

import (
	"slices"
	"strings"
	"testing"
)

// TestEngineArgsOffMeansOff is the rule from CLAUDE.md applied to this hook:
// with nothing set, the command line must be the one upstream would have
// produced -- not an equal copy of it, the same slice, because a copy here
// would mean this hook runs on every stock load for no reason.
func TestEngineArgsOffMeansOff(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n "} {
		args := []string{"--model", "/m.gguf", "-c", "4096"}
		got, err := appendEngineArgs(args, raw)
		if err != nil {
			t.Fatalf("appendEngineArgs(%q) = %v", raw, err)
		}
		if !slices.Equal(got, args) {
			t.Errorf("appendEngineArgs(%q) = %v, want the argv unchanged", raw, got)
		}
		if len(got) > 0 && &got[0] != &args[0] {
			t.Errorf("appendEngineArgs(%q) copied the argv; with nothing set it should return it untouched", raw)
		}
	}
}

func TestEngineArgsAppendsAfterEverythingElse(t *testing.T) {
	args := []string{"--model", "/m.gguf", "--override-kv", "a=int:1"}
	got, err := appendEngineArgs(args, "--override-kv b=int:2 --polykv-adm-on-error")
	if err != nil {
		t.Fatalf("appendEngineArgs() = %v", err)
	}
	want := []string{"--model", "/m.gguf", "--override-kv", "a=int:1", "--override-kv", "b=int:2", "--polykv-adm-on-error"}
	if !slices.Equal(got, want) {
		t.Errorf("appendEngineArgs() = %v, want %v", got, want)
	}
	// The point of appending last: llama-server takes the final occurrence, so
	// the operator's --override-kv has to come after the one xollama wrote.
	first := slices.Index(got, "a=int:1")
	second := slices.Index(got, "b=int:2")
	if first > second {
		t.Errorf("the operator's value landed at %d, before xollama's at %d; last-wins needs it after", second, first)
	}
}

// TestEngineArgsDoesNotWriteIntoTheCallersArray gives args spare capacity on
// purpose. Without it, append() allocates a new array anyway and the test
// passes whether or not the defensive copy exists -- the same
// test-that-cannot-fail shape that bit this repo before (.wolf/cerebrum.md,
// 2026-09-19). startLlamaServer builds argv with repeated appends, so spare
// capacity at this point is the normal case, not a contrived one.
func TestEngineArgsDoesNotWriteIntoTheCallersArray(t *testing.T) {
	backing := make([]string, 2, 8)
	backing[0], backing[1] = "--model", "/m.gguf"
	sentinel := backing[:4]
	sentinel[2], sentinel[3] = "KEEP-2", "KEEP-3"

	if _, err := appendEngineArgs(backing, "--flash-attn on"); err != nil {
		t.Fatalf("appendEngineArgs() = %v", err)
	}
	if sentinel[2] != "KEEP-2" || sentinel[3] != "KEEP-3" {
		t.Errorf("appendEngineArgs() wrote through the caller's backing array: %v", sentinel[2:4])
	}
}

func TestSplitEngineArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "--a --b", []string{"--a", "--b"}},
		{"runs of space", "  --a    --b  ", []string{"--a", "--b"}},
		{"tabs and newlines", "--a\t--b\n--c", []string{"--a", "--b", "--c"}},
		{"double quotes group", `--chat-template "a b c"`, []string{"--chat-template", "a b c"}},
		{"single quotes group", `--chat-template 'a b c'`, []string{"--chat-template", "a b c"}},
		{"quote inside a token", `--kv=a" "b`, []string{`--kv=a b`}},
		{"the other quote is literal", `--x "it's"`, []string{"--x", "it's"}},
		{"deliberate empty argument", `--x ""`, []string{"--x", ""}},
		// A backslash is literal, so a Windows path survives unquoted. This is
		// the case that decided against escape handling.
		{"windows path", `--spec-draft-model C:\models\d.gguf`, []string{"--spec-draft-model", `C:\models\d.gguf`}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitEngineArgs(tt.in)
			if err != nil {
				t.Fatalf("splitEngineArgs(%q) = %v", tt.in, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("splitEngineArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEngineArgsRejectsAnUnterminatedQuote(t *testing.T) {
	// Silently closing it would hand the engine an argument the operator did
	// not write, which is the one thing a passthrough must never do.
	if _, err := appendEngineArgs(nil, `--chat-template "a b`); err == nil {
		t.Error("appendEngineArgs() = nil error, want a rejection of the unterminated quote")
	} else if !strings.Contains(err.Error(), EngineArgsEnv) {
		t.Errorf("error %q does not name %s, so nobody can tell where the value came from", err, EngineArgsEnv)
	}
}

// TestEngineArgsRefusesWhatXollamaDerivesItself is the substance of the guard.
// These are not refused because the engine would reject them -- it would take
// every one -- but because xollama's own model of the process is built from
// them. An estimate that describes a process that does not exist fails later,
// somewhere else, as an OOM nobody can trace back to here.
func TestEngineArgsRefusesWhatXollamaDerivesItself(t *testing.T) {
	cases := []string{
		"-c 8192",
		"--ctx-size 8192",
		"--ctx-size=8192",
		"-np 8",
		"--parallel 8",
		"-ngl 99",
		"--gpu-layers 99",
		"--n-gpu-layers 99",
		"-b 2048",
		"--batch-size 2048",
		"-ub 512",
		"--ubatch-size=512",
		"-m /other.gguf",
		"--model /other.gguf",
		"--host 0.0.0.0",
		"--port 9999",
		"-lv 4",
		"--log-verbosity 4",
		"--verbosity 4",
		"--log-disable",
		"--log-file /tmp/x.log",
		// Buried behind an allowed flag, it is still the same override.
		"--override-kv a=int:1 -c 8192",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got, err := appendEngineArgs([]string{"--model", "/m.gguf"}, raw)
			if err == nil {
				t.Fatalf("appendEngineArgs(%q) = %v, nil; want a refusal", raw, got)
			}
			if !strings.Contains(err.Error(), EngineArgsEnv) {
				t.Errorf("error %q does not name %s", err, EngineArgsEnv)
			}
			flag := strings.Fields(raw)[len(strings.Fields(raw))-1]
			flag, _, _ = strings.Cut(flag, "=")
			if strings.HasPrefix(flag, "-") && !strings.Contains(err.Error(), flag) {
				t.Errorf("error %q does not name the offending flag %s", err, flag)
			}
		})
	}
}

// Every guarded spelling must be refused in the --flag=value form as well as
// the --flag value form, because the engine treats them as one flag and a
// guard that only saw one of them would be trivially bypassed.
func TestEveryGuardedFlagIsRefusedInBothForms(t *testing.T) {
	for flag := range guardedEngineFlags {
		t.Run(flag, func(t *testing.T) {
			for _, raw := range []string{flag + " v", flag + "=v"} {
				if _, err := appendEngineArgs(nil, raw); err == nil {
					t.Errorf("appendEngineArgs(%q) was accepted; %s is guarded", raw, flag)
				}
			}
		})
	}
}

// The flags this hatch exists for must actually get through -- a guard list
// that swallowed --override-kv would defeat the whole gap it closes.
func TestEngineArgsLetsTheFlagsItExistsForThrough(t *testing.T) {
	for _, raw := range []string{
		"--override-kv qwen3.context_length=int:262144",
		"--polykv-adm-on-error",
		"--dca-chunk-size 32768",
		"--cache-reuse 256",
		"--slot-save-path /var/lib/xollama/slots",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := appendEngineArgs([]string{"--model", "/m.gguf"}, raw)
			if err != nil {
				t.Fatalf("appendEngineArgs(%q) = %v", raw, err)
			}
			want, _ := splitEngineArgs(raw)
			if !slices.Equal(got[2:], want) {
				t.Errorf("appendEngineArgs(%q) appended %q, want %q", raw, got[2:], want)
			}
		})
	}
}
