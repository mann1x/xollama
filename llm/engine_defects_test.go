package llm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/llm/engine"
)

// artifactWithDigest writes a stand-in engine artifact. The real one is 678 MB
// and not in the tree, so tests write the bytes they need and derive the digest
// from them rather than carrying a constant that could drift.
func artifactWithDigest(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "opencoti-llamafile-0.10.5-c7-x86_64.llamafile")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// defectiveArtifact writes a stand-in for the defective build and points the
// defect row at its digest for the duration of the test, so the matching logic
// is exercised without a 678 MB fixture.
func defectiveArtifact(t *testing.T) string {
	t.Helper()

	path := artifactWithDigest(t, "pretend this is opencoti c7")
	digest := fileDigest(path)
	if digest == "" {
		t.Fatal("could not hash the fixture")
	}

	original := knownEngineDefects
	patched := make([]knownEngineDefect, len(original))
	copy(patched, original)
	patched[0].SHA256 = []string{digest}
	knownEngineDefects = patched
	t.Cleanup(func() { knownEngineDefects = original })

	return path
}

func TestDescribeEngineDefect(t *testing.T) {
	t.Run("the assertion opencoti reported", func(t *testing.T) {
		path := defectiveArtifact(t)
		if describeEngineDefect(path, "fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed") == "" {
			t.Fatal("no explanation for a failure that matches a known defect")
		}
	})

	t.Run("the abort our own evaluation hit", func(t *testing.T) {
		// The same defect as Phase 2 measured it, before it had a name: it
		// surfaced earlier, during CPU KV placement.
		path := defectiveArtifact(t)
		if describeEngineDefect(path, "ggml_new_object: not enough space in the context's memory pool (needed 118128, available 117760)") == "" {
			t.Fatal("no explanation for the abort our own evaluation measured")
		}
	})

	t.Run("the same build failing for an unrelated reason", func(t *testing.T) {
		// The build alone would blame it for everything that goes wrong while
		// someone is running it.
		path := defectiveArtifact(t)
		if got := describeEngineDefect(path, "error loading model: unknown model architecture: 'bananaformer'"); got != "" {
			t.Fatalf("explained an unrelated failure: %s", got)
		}
	})

	t.Run("a re-published build that carries the fix", func(t *testing.T) {
		// This is why the row is keyed on bytes. A cut can be re-published
		// under the same file name and tag once it is fixed; matching the name
		// would go on blaming a build that no longer has the defect.
		defectiveArtifact(t)
		fixed := artifactWithDigest(t, "the same name, the fix inside")
		if got := describeEngineDefect(fixed, "fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed"); got != "" {
			t.Fatalf("blamed a re-published build that carries the fix: %s", got)
		}
	})

	t.Run("an artifact that cannot be read", func(t *testing.T) {
		defectiveArtifact(t)
		if got := describeEngineDefect("/nonexistent/engine", "GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed"); got != "" {
			t.Fatalf("accused a build it could not even read: %s", got)
		}
	})

	t.Run("nothing at all", func(t *testing.T) {
		path := defectiveArtifact(t)
		if got := describeEngineDefect(path, ""); got != "" {
			t.Fatalf("explained an empty failure: %s", got)
		}
	})
}

// TestDescribeEngineDefectSaysWhatToDo is the whole point: a message that names
// a defect without naming a way out leaves the reader where they were.
func TestDescribeEngineDefectSaysWhatToDo(t *testing.T) {
	path := defectiveArtifact(t)

	got := describeEngineDefect(path, "GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed")
	if got == "" {
		t.Fatal("no explanation")
	}
	for _, want := range []string{"num_ctx", "KV_CACHE_TYPE", "known defect"} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation does not mention %q: %s", want, got)
		}
	}
	// It must not leave the reader thinking their model is at fault, which is
	// what an abort during model load otherwise suggests.
	if !strings.Contains(got, "not a problem with your model") {
		t.Errorf("explanation should say the model is not at fault: %s", got)
	}
}

func TestAnnotateEngineDefect(t *testing.T) {
	loadErr := errors.New("llama-server process has terminated: fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed")

	t.Run("wraps rather than replaces", func(t *testing.T) {
		path := defectiveArtifact(t)
		s := &llamaServerRunner{usedOpencoti: true, cmd: &exec.Cmd{Path: path}}

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
		path := defectiveArtifact(t)
		s := &llamaServerRunner{cmd: &exec.Cmd{Path: path}}
		if got := s.annotateEngineDefect(loadErr); got != loadErr {
			t.Errorf("annotated a stock load: %v", got)
		}
	})

	t.Run("a nil error stays nil", func(t *testing.T) {
		path := defectiveArtifact(t)
		s := &llamaServerRunner{usedOpencoti: true, cmd: &exec.Cmd{Path: path}}
		if got := s.annotateEngineDefect(nil); got != nil {
			t.Errorf("annotateEngineDefect(nil) = %v", got)
		}
	})

	t.Run("no process to blame", func(t *testing.T) {
		defectiveArtifact(t)
		s := &llamaServerRunner{usedOpencoti: true}
		if got := s.annotateEngineDefect(loadErr); got != loadErr {
			t.Errorf("annotated with no command: %v", got)
		}
	})
}

// TestKnownDefectsMatchThePinnedArtifact keeps the table honest: a row exists
// because we ship those bytes. When the pin moves to an artifact without the
// defect -- including a RE-PUBLISHED cut of the same name -- this fails, which
// is the reminder to retire the row rather than leave an accusation about bytes
// nobody runs.
func TestKnownDefectsMatchThePinnedArtifact(t *testing.T) {
	pin, err := engine.DefaultPin()
	if err != nil {
		t.Fatalf("the compiled-in engine pin does not parse: %v", err)
	}

	// This is a release-channel invariant. A development pin deliberately
	// carries different bytes -- build 18 of the c7 dev line includes patch
	// 0253, which is the fix for the row below -- and the rows describing the
	// release artifact must not be deleted just because a dev branch is
	// pointed somewhere else. They are inert there: a row can only ever speak
	// when its sha256 matches the artifact that actually failed.
	if pin.Channel != engine.ChannelRelease {
		t.Skipf("pin is on the %s channel (%s); retiring rows is a release-channel decision", pin.Channel, pin.Tag)
	}

	pinned := make([]string, 0, len(pin.Assets))
	for _, a := range pin.Assets {
		pinned = append(pinned, strings.ToLower(a.SHA256))
	}

	for _, d := range knownEngineDefects {
		var shipped bool
		for _, sum := range d.SHA256 {
			if containsAny(strings.Join(pinned, " "), []string{strings.ToLower(sum)}) {
				shipped = true
				break
			}
		}
		if !shipped {
			t.Errorf("known-defect row %v describes bytes that are no longer pinned — retire it", d.SHA256)
		}
	}
}
