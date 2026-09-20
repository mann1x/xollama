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
	{
		// The partial-offload abort, RE-INSTATED 2026-09-20 after measuring it.
		//
		// It was retired earlier the same day along with opencoti bug-3369, on
		// the assumption that their patch 0253 -- the whole difference between
		// c7 and c7 r2 -- fixed this too, because they had told us the two
		// failures were the same bug reaching us by different paths. Retaking
		// the Phase 2 overflow axis on the r2 bytes disproved that: with
		// llama3.1:70b-instruct-q3_K_S on a 24 GiB card, r2 aborts in 2.7 s
		// with byte-identical numbers to r1 (needed 118128, available 117760)
		// while placing KV cache layers 30..53 on the CPU. Stock llama.cpp
		// loads the same model on the same host at 56.8% resident, and r2
		// itself serves a model that fits at 75.5 tok/s, so it is this path
		// and not the artifact. Measurements in
		// docs/evaluations/phase2-engine-ab.md.
		//
		// Narrowed the same day, after opencoti asked (their bug-3515): the
		// failing load logs "rolling-kv POSITION_WINDOW mode ON
		// (--kv-residency-mode auto) -- window 256 / 32768 cells", and
		// forcing the other tactic with LLAMA_ARG_KV_RESIDENCY_MODE=head
		// loads the same model on the same card at 2.85 tok/s -- faster than
		// stock llama.cpp's 2.71 on that arm. So the defect is in the
		// POSITION_WINDOW path, not in partial offload as such, and the
		// workaround below keeps the user on this engine rather than off it.
		// It also explains why opencoti could not reproduce it on a roomy
		// card: auto only picks the window under real VRAM pressure.
		//
		// So they are two defects, not one. bug-3369's own signature is gone
		// from r2 and is deliberately NOT listed below -- accusing bytes of a
		// fault nobody has shown they still have is exactly what the sha256
		// half of this table exists to prevent.
		SHA256: []string{"4f4102d6d8dd39bf794dee4f4d9000120766fd1fccc42090feff2e710a48104e"},
		Signatures: []string{
			"ggml_new_object: not enough space in the context's memory pool",
		},
		Summary: "this build of the opencoti engine (0.10.5-c7 r2) aborts while placing KV cache layers on the CPU, under the rolling-KV POSITION_WINDOW residency tactic that --kv-residency-mode auto selects when a model is too large for VRAM",
		Workaround: "set LLAMA_ARG_KV_RESIDENCY_MODE=head, which forces the other residency " +
			"tactic and is measured to load the same model on the same card. Failing that, keep " +
			"the load resident -- a smaller model or quantisation, a lower num_ctx, or a " +
			"compressed cache with OLLAMA_KV_CACHE_TYPE=q8_0 (or XOLLAMA_K_CACHE_TYPE / " +
			"XOLLAMA_V_CACHE_TYPE per half). Stock llama.cpp serves it too, with " +
			"XOLLAMA_ENGINE=llamacpp",
	},
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
