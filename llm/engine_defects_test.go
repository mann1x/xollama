package llm

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/ollama/ollama/llm/engine"
)

const c7Artifact = "/usr/local/lib/ollama/opencoti-llamafile-0.10.5-c7-x86_64.llamafile"

func TestDescribeEngineDefect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		artifact string
		output   string
		want     bool
	}{
		{
			name:     "the assertion opencoti reported",
			artifact: c7Artifact,
			output:   "fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed",
			want:     true,
		},
		{
			// The same defect as our own Phase 2 measured it, before it had a
			// name: it surfaced earlier, during CPU KV placement.
			name:     "the abort our own evaluation hit",
			artifact: c7Artifact,
			output:   "ggml_new_object: not enough space in the context's memory pool (needed 118128, available 117760)",
			want:     true,
		},
		{
			name:     "windows artifact, same cut",
			artifact: `C:\Program Files\xollama\opencoti-llamafile-0.10.5-c7-win-x86_64.llamafile.exe`,
			output:   "fattn-common.cuh:87: GGML_ASSERT failed",
			want:     true,
		},
		{
			// Version without signature would blame this build for every
			// unrelated failure anyone has while running it.
			name:     "the same build failing for an unrelated reason",
			artifact: c7Artifact,
			output:   "error loading model: unknown model architecture: 'bananaformer'",
		},
		{
			// Signature without version would blame a defect that a newer
			// artifact has already fixed.
			name:     "the same crash on a build that has the fix",
			artifact: "/usr/local/lib/ollama/opencoti-llamafile-0.10.6-c8-x86_64.llamafile",
			output:   "fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed",
		},
		{
			name:     "stock llama-server",
			artifact: "/usr/local/lib/ollama/llama-server",
			output:   "ggml_new_object: not enough space in the context's memory pool",
		},
		{
			name:     "nothing at all",
			artifact: c7Artifact,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := describeEngineDefect(tc.artifact, tc.output)
			if tc.want && got == "" {
				t.Fatal("no explanation for a failure that matches a known defect")
			}
			if !tc.want && got != "" {
				t.Fatalf("explained an unrelated failure: %s", got)
			}
		})
	}
}

// TestDescribeEngineDefectSaysWhatToDo is the whole point: a message that names
// a defect without naming a way out leaves the reader exactly where they were.
func TestDescribeEngineDefectSaysWhatToDo(t *testing.T) {
	got := describeEngineDefect(c7Artifact, "GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed")
	if got == "" {
		t.Fatal("no explanation")
	}
	for _, want := range []string{"num_ctx", "KV_CACHE_TYPE", "known defect"} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation does not mention %q: %s", want, got)
		}
	}
	// It must not claim the model is at fault, which is the wrong conclusion a
	// reader would otherwise draw from an abort during model load.
	if !strings.Contains(got, "not a problem with your model") {
		t.Errorf("explanation should say the model is not at fault: %s", got)
	}
}

func TestAnnotateEngineDefect(t *testing.T) {
	loadErr := errors.New("llama-server process has terminated: fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed")

	t.Run("wraps rather than replaces", func(t *testing.T) {
		s := &llamaServerRunner{usedOpencoti: true, cmd: &exec.Cmd{Path: c7Artifact}}

		got := s.annotateEngineDefect(loadErr)
		// Downstream code inspects load errors; replacing one would break that.
		if !errors.Is(got, loadErr) {
			t.Error("the original error must stay unwrapped-to, not be replaced")
		}
		if !strings.Contains(got.Error(), "known defect") {
			t.Errorf("no explanation added: %v", got)
		}
	})

	t.Run("stock llama.cpp is never annotated", func(t *testing.T) {
		s := &llamaServerRunner{cmd: &exec.Cmd{Path: c7Artifact}}
		if got := s.annotateEngineDefect(loadErr); got != loadErr {
			t.Errorf("annotated a stock load: %v", got)
		}
	})

	t.Run("a nil error stays nil", func(t *testing.T) {
		s := &llamaServerRunner{usedOpencoti: true, cmd: &exec.Cmd{Path: c7Artifact}}
		if got := s.annotateEngineDefect(nil); got != nil {
			t.Errorf("annotateEngineDefect(nil) = %v", got)
		}
	})

	t.Run("no process to blame", func(t *testing.T) {
		s := &llamaServerRunner{usedOpencoti: true}
		if got := s.annotateEngineDefect(loadErr); got != loadErr {
			t.Errorf("annotated with no command: %v", got)
		}
	})
}

// TestKnownDefectsMatchThePinnedArtifact keeps the table honest: the row exists
// because we ship that build. When the pin moves to a cut without the defect
// this fails, which is the reminder to retire the row rather than leave a
// message about a build nobody runs.
func TestKnownDefectsMatchThePinnedArtifact(t *testing.T) {
	pin, err := engine.DefaultPin()
	if err != nil {
		t.Fatalf("the compiled-in engine pin does not parse: %v", err)
	}

	pinned := strings.ToLower(pin.Tag)
	for _, a := range pin.Assets {
		pinned += " " + strings.ToLower(a.Path)
	}

	var stale []string
	for _, d := range knownEngineDefects {
		if !containsAny(pinned, d.Versions) {
			stale = append(stale, strings.Join(d.Versions, "/"))
		}
	}
	if len(stale) > 0 {
		t.Errorf("known-defect rows describe builds that are no longer pinned: %v — retire them", stale)
	}
}
