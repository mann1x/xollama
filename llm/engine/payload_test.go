package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// artifactFile writes a stand-in engine artifact and returns its path. The
// real one is 678 MB, so the tests work on bytes they can afford to hash.
func artifactFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// payloadTree fakes what the engine unpacks, so a purge has something to take.
func payloadTree(t *testing.T, root, marker string) string {
	t.Helper()
	dir := filepath.Join(root, payloadDirName, "v", "opencoti-0.10.5-c7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dso := filepath.Join(dir, "ggml-cuda.so")
	if err := os.WriteFile(dso, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	return dso
}

func TestPayloadHomeIsPreparedAndRecorded(t *testing.T) {
	dir := t.TempDir()
	art := artifactFile(t, dir, "opencoti-llamafile-0.10.5-c7-x86_64.llamafile", "r2 bytes")
	root := filepath.Join(dir, "root")

	got := PreparePayloadHome(art, root)
	if got != root {
		t.Fatalf("PreparePayloadHome() = %q, want %q", got, root)
	}
	if _, err := os.Stat(filepath.Join(root, payloadMarker)); err != nil {
		t.Errorf("no ownership marker written: %v", err)
	}
}

// TestPayloadFromAnotherArtifactIsPurged is the whole point: two builds share
// one compile-time version directory, so the only thing standing between an r2
// binary and r1's kernels is this purge.
func TestPayloadFromAnotherArtifactIsPurged(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")

	r1 := artifactFile(t, dir, "r1.llamafile", "r1 bytes")
	PreparePayloadHome(r1, root)
	stale := payloadTree(t, root, "r1 kernels")

	// A different artifact entirely -- the in-place re-cut case is a different
	// file at the same path, covered below.
	r2 := artifactFile(t, dir, "r2.llamafile", "r2 bytes, different length")
	if got := PreparePayloadHome(r2, root); got != root {
		t.Fatalf("PreparePayloadHome() = %q, want %q", got, root)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the previous artifact's payload survived: %v", err)
	}
}

// TestInPlaceRecutIsPurged is the case that actually happened. opencoti
// replaces a release's bytes under the SAME file name, so the path never
// changes and only the content does.
func TestInPlaceRecutIsPurged(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	name := "opencoti-llamafile-0.10.5-c7-x86_64.llamafile"

	art := artifactFile(t, dir, name, "r1 bytes")
	PreparePayloadHome(art, root)
	stale := payloadTree(t, root, "r1 kernels")

	// Re-cut in place: same name, different bytes, later mtime.
	if err := os.WriteFile(art, []byte("r2 bytes, re-cut in place"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(art, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	PreparePayloadHome(art, root)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an in-place re-cut kept the previous payload; this is the collision the directory exists to prevent")
	}
}

// TestUnchangedArtifactKeepsItsPayload guards the other direction. Purging
// every launch would re-unpack 700 MB each time a model loads, and would make
// the feature look like a performance bug.
func TestUnchangedArtifactKeepsItsPayload(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	art := artifactFile(t, dir, "engine.llamafile", "same bytes throughout")

	PreparePayloadHome(art, root)
	kept := payloadTree(t, root, "its own kernels")

	for i := range 3 {
		if got := PreparePayloadHome(art, root); got != root {
			t.Fatalf("call %d: PreparePayloadHome() = %q", i, got)
		}
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("call %d purged a payload belonging to the same artifact: %v", i, err)
		}
	}
}

// TestTouchedArtifactWithSameBytesKeepsItsPayload covers a re-download or a
// copy that moves the timestamp without changing the build. The cheap check
// disagrees; the digest is what settles it, and nothing should be thrown away.
func TestTouchedArtifactWithSameBytesKeepsItsPayload(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	art := artifactFile(t, dir, "engine.llamafile", "identical bytes")

	PreparePayloadHome(art, root)
	kept := payloadTree(t, root, "its own kernels")

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(art, later, later); err != nil {
		t.Fatal(err)
	}

	PreparePayloadHome(art, root)
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("a re-timestamped but byte-identical artifact lost its payload: %v", err)
	}
}

// TestPurgeIsScopedToWhatWeCreate: the root is a directory we name, but a
// misconfiguration could point it anywhere, and deleting a user's files would
// be far worse than any collision.
func TestPurgeIsScopedToWhatWeCreate(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")

	r1 := artifactFile(t, dir, "r1.llamafile", "r1")
	PreparePayloadHome(r1, root)
	payloadTree(t, root, "r1 kernels")

	bystander := filepath.Join(root, "important.txt")
	if err := os.WriteFile(bystander, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	r2 := artifactFile(t, dir, "r2.llamafile", "r2 different")
	PreparePayloadHome(r2, root)

	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("the purge removed a file outside %s/: %v", payloadDirName, err)
	}
}

func TestPayloadHomeDeclinesRatherThanBreakingTheLaunch(t *testing.T) {
	dir := t.TempDir()
	art := artifactFile(t, dir, "engine.llamafile", "bytes")

	if got := PreparePayloadHome("", filepath.Join(dir, "root")); got != "" {
		t.Errorf("no artifact: got %q, want \"\"", got)
	}
	if got := PreparePayloadHome(art, ""); got != "" {
		t.Errorf("no root: got %q, want \"\"", got)
	}
	// A missing artifact must not be turned into a launch failure here; the
	// launch itself will fail with a better message.
	if got := PreparePayloadHome(filepath.Join(dir, "nope"), filepath.Join(dir, "root2")); got != "" {
		t.Errorf("missing artifact: got %q, want \"\"", got)
	}
}

func TestDefaultPayloadRootPrefersOllamasOwnDirectory(t *testing.T) {
	// Never the user's ~/.llamafile: that is the directory they use for their
	// own opencoti builds, and staying out of it is the point.
	got := DefaultPayloadRoot("/usr/local/lib/ollama", "/home/u")
	if want := filepath.Join("/home/u", ".ollama", "engines", "payload"); got != want {
		t.Errorf("DefaultPayloadRoot() = %q, want %q", got, want)
	}
	if got := DefaultPayloadRoot("/usr/local/lib/ollama", ""); got != filepath.Join("/usr/local/lib/ollama", "engines", "payload") {
		t.Errorf("no home: DefaultPayloadRoot() = %q", got)
	}
	if got := DefaultPayloadRoot("", ""); got != "" {
		t.Errorf("nothing to go on: DefaultPayloadRoot() = %q, want \"\"", got)
	}
}

// TestStatIsTrustedInTheSteadyState pins the ONE case where the two checks
// disagree, so neither can hide behind the other.
//
// Content is replaced while size and modification time are held equal. The
// cheap check says "same artifact" and keeps the payload; the digest check,
// had it run, would have said "different bytes" and purged. Current behaviour
// is to keep, and that is a deliberate trade: the steady state costs one stat
// instead of hashing 700 MB on every model load, and an artifact rewritten to
// the same length AND back-dated to the same nanosecond is not something any
// publishing process does by accident.
//
// This is the only fixture that distinguishes the fast path from the digest
// path. Without it, deleting the fast path entirely leaves every other test
// passing -- the failure mode this repo has now hit four times.
func TestStatIsTrustedInTheSteadyState(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	art := artifactFile(t, dir, "engine.llamafile", "aaaaaaaa")

	fi, err := os.Stat(art)
	if err != nil {
		t.Fatal(err)
	}
	PreparePayloadHome(art, root)
	kept := payloadTree(t, root, "kernels")

	// Same length, different bytes, timestamp restored exactly.
	if err := os.WriteFile(art, []byte("bbbbbbbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(art, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	PreparePayloadHome(art, root)
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the fast path did not short-circuit: a same-size same-mtime artifact was hashed and purged (%v)", err)
	}
}
