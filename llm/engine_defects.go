package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// xollama-hook: engine-defects
//
// Naming a defect in an engine build we ship and cannot patch.
//
// The engine artifact is pinned by sha and bundled into the release payload, so
// when a published cut carries a bug there is nothing to upgrade to until the
// next one is published. What is still in our gift is the failure message. An
// abort from inside somebody else's CUDA kernel is unreadable:
//
//	fattn-common.cuh:87: GGML_ASSERT(dst->op == GGML_OP_FLASH_ATTN_EXT) failed
//
// Nobody meeting that can tell whether their model is broken, their card is
// broken, their settings are wrong, or the engine has a known bug with a known
// workaround. This turns the ones we know about into the last of those.
//
// This is diagnosis, never recovery. It does not retry, downgrade, or change
// what was launched -- a load that fails still fails, with the same error,
// wrapped in the explanation. Falling back silently is what XOLLAMA_ENGINE_FALLBACK
// is for, and it is off by default for the reason given on retryOnStockEngine.

// knownEngineDefect is one defect in a published artifact.
//
// Both halves must match before anything is said. The build alone would blame
// it for every unrelated failure a user has while running it; the signature
// alone would blame a defect that a newer artifact has already fixed. Together
// they are specific enough that the message can be stated as fact.
type knownEngineDefect struct {
	// SHA256 identifies the defective bytes, and it is bytes rather than a file
	// name for a reason learned the hard way: a cut can be RE-PUBLISHED under
	// the same name and tag once it is fixed. Matching the name would then go
	// on blaming a build that no longer has the defect, and the accusation
	// would be invisible to everyone except the person whose working load was
	// being explained away.
	//
	// The cost is hashing the artifact, which is why it happens only after a
	// load has already failed and only once per runner.
	SHA256 []string
	// Signatures are substrings matched against the engine's own dying words.
	Signatures []string
	// Summary says what is wrong, and Workaround what to do instead. Both are
	// written to be read by someone who has just had a load fail and does not
	// know any of this.
	Summary    string
	Workaround string
}

// knownEngineDefects is the table. A row is retired the day a published
// artifact without the defect is pinned -- it describes a specific build, not a
// permanent property of the engine.
var knownEngineDefects = []knownEngineDefect{
	// EMPTY, and that is the healthy state. A row is an accusation against
	// specific bytes, so it is retired the day a published artifact without
	// the defect is pinned.
	//
	// Retired 2026-09-20: opencoti bug-3369, a 0.10.5-port regression in the
	// streaming-attention fallbacks that aborted as soon as the KV cache began
	// spilling to host memory, on either cache layout. It was carried here
	// because c7 was the only published cut and there was nothing to re-pin
	// to; the c7 r2 re-cut is that cut plus patch 0253, which is the fix, and
	// llm/engine/pin.txt now points at it. The exact bytes it described, the
	// signatures it matched and the workaround it gave live on as the fixture
	// in llm/engine_defects_test.go, so the machinery stays tested with the
	// table empty -- which is the condition it has to work in.
}

// describeEngineDefect returns an explanation when a failure matches a known
// defect in the artifact that produced it, or "" when it does not.
//
// artifact is the engine binary's path; output is whatever the engine said on
// its way down. Matching is case-insensitive on the signature because the same
// assertion reaches us through several log paths.
//
// The signature is checked first and the artifact is hashed only if one matches,
// so the common failure -- a bad model, a missing file, out of memory -- costs
// nothing.
func describeEngineDefect(artifact, output string) string {
	lower := strings.ToLower(output)

	var digest string
	var hashed bool

	for _, d := range knownEngineDefects {
		if !containsAny(lower, d.Signatures) {
			continue
		}
		if !hashed {
			digest, hashed = fileDigest(artifact), true
		}
		if digest == "" || !containsAny(digest, d.SHA256) {
			continue
		}
		return fmt.Sprintf("this is a known defect in the engine build in use, not a problem with your model: %s. Until a fixed build is published, %s", d.Summary, d.Workaround)
	}
	return ""
}

// fileDigest is the sha256 of a file, lower case, or "" if it cannot be read.
//
// An unreadable artifact means no accusation is made, which is the right way
// round: the cost of staying quiet is an unexplained error the user would have
// had anyway, and the cost of guessing is telling someone their working build
// is broken.
func fileDigest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		slog.Debug("could not hash the engine artifact to check it against known defects", "path", path, "error", err)
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		slog.Debug("could not hash the engine artifact to check it against known defects", "path", path, "error", err)
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// containsAny reports whether s contains any of the needles, which are compared
// in lower case because the callers lower s.
func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// annotateEngineDefect wraps a load failure with an explanation when it matches
// a known defect. The original error is preserved underneath, wrapped rather
// than replaced, so nothing downstream that inspects it stops working.
func (s *llamaServerRunner) annotateEngineDefect(err error) error {
	if err == nil || !s.usedOpencoti || s.cmd == nil {
		return err
	}
	// The engine's own output reaches us inside the error the load returned;
	// the status writer holds it too when the error was built before the line
	// arrived.
	why := describeEngineDefect(s.cmd.Path, err.Error()+"\n"+s.status.LastError())
	if why == "" {
		return err
	}
	return fmt.Errorf("%w — %s", err, why)
}
