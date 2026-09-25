package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if got := PreparePayloadHome(art); got != "" {
		t.Errorf("no root: got %q, want \"\"", got)
	}
	if got := PreparePayloadHome(art, ""); got != "" {
		t.Errorf("empty root: got %q, want \"\"", got)
	}
	// A missing artifact must not be turned into a launch failure here; the
	// launch itself will fail with a better message.
	if got := PreparePayloadHome(filepath.Join(dir, "nope"), filepath.Join(dir, "root2")); got != "" {
		t.Errorf("missing artifact: got %q, want \"\"", got)
	}
}

// TestDefaultPayloadRootsPreferOllamasRuntimeDirectory: the payload is part of
// the engine's runtime, so it belongs beside the rest of the runtime --
// <install>/lib/ollama, next to llama-server and the ggml backends -- and not
// in the home directory of whoever happens to have started the server, which on
// a Linux service is root.
func TestDefaultPayloadRootsPreferOllamasRuntimeDirectory(t *testing.T) {
	got := DefaultPayloadRoots("/usr/local/lib/ollama", "/home/u")
	want := []string{
		filepath.Join("/usr/local/lib/ollama", "engines", "payload"),
		filepath.Join("/home/u", ".ollama", "engines", "payload"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("DefaultPayloadRoots() = %q, want %q", got, want)
	}

	// Never the user's ~/.llamafile: that is where they keep their own
	// opencoti builds, and staying out of it is the point.
	for _, root := range got {
		if strings.Contains(root, payloadDirName) {
			t.Errorf("DefaultPayloadRoots() offered %q, which is the user's own payload directory", root)
		}
	}

	if got := DefaultPayloadRoots("", "/home/u"); len(got) != 1 || got[0] != filepath.Join("/home/u", ".ollama", "engines", "payload") {
		t.Errorf("no lib dir: DefaultPayloadRoots() = %q", got)
	}
	if got := DefaultPayloadRoots("/usr/local/lib/ollama", ""); len(got) != 1 || got[0] != filepath.Join("/usr/local/lib/ollama", "engines", "payload") {
		t.Errorf("no home: DefaultPayloadRoots() = %q", got)
	}
	if got := DefaultPayloadRoots("", ""); len(got) != 0 {
		t.Errorf("nothing to go on: DefaultPayloadRoots() = %q, want none", got)
	}
}

// TestAnUnwritableRootFallsBackToTheNextOne is the reason the preference is a
// list and not a choice. A packaged Linux install leaves <install>/lib/ollama
// owned by root while the service runs as `ollama`, and a macOS install puts it
// inside a signed app bundle. Preferring it unconditionally would mean no
// isolation at all on two of the three platforms.
//
// The fixture makes the root unusable by putting a regular FILE where its
// parent directory would be, rather than by removing write permission: these
// tests also run as root on this host, and root is not stopped by a mode bit.
func TestAnUnwritableRootFallsBackToTheNextOne(t *testing.T) {
	dir := t.TempDir()
	art := artifactFile(t, dir, "engine.llamafile", "bytes")

	blocked := filepath.Join(dir, "lib")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	preferred := filepath.Join(blocked, "engines", "payload")
	fallback := filepath.Join(dir, "home", ".ollama", "engines", "payload")

	got := PreparePayloadHome(art, preferred, fallback)
	if got != fallback {
		t.Fatalf("PreparePayloadHome() = %q, want the fallback %q", got, fallback)
	}
	if _, err := os.Stat(filepath.Join(fallback, payloadMarker)); err != nil {
		t.Errorf("the fallback root was returned but not prepared: %v", err)
	}
}

// TestTheFirstUsableRootWins: the fallback exists for when the preferred root
// cannot be used, and must not be reached when it can.
func TestTheFirstUsableRootWins(t *testing.T) {
	dir := t.TempDir()
	art := artifactFile(t, dir, "engine.llamafile", "bytes")
	preferred := filepath.Join(dir, "lib", "engines", "payload")
	fallback := filepath.Join(dir, "home", ".ollama", "engines", "payload")

	if got := PreparePayloadHome(art, preferred, fallback); got != preferred {
		t.Fatalf("PreparePayloadHome() = %q, want %q", got, preferred)
	}
	if _, err := os.Stat(fallback); !os.IsNotExist(err) {
		t.Errorf("the fallback root was created although the preferred one worked: %v", err)
	}
}

// TestARootWeCanCreateButNotWriteIsSkippedBeforeAnyWork is the Linux service
// case exactly: /usr/local/lib/ollama already exists, MkdirAll succeeds on it,
// and the first actual write is what fails -- by which point, without the
// probe, the 678 MB artifact has been hashed for a directory about to be
// abandoned, on every model load for the life of the installation.
//
// The unwritable root is injected rather than built, because these tests run as
// root on the host this was measured on and root ignores the mode bits that
// would otherwise express it.
func TestARootWeCanCreateButNotWriteIsSkippedBeforeAnyWork(t *testing.T) {
	dir := t.TempDir()
	art := artifactFile(t, dir, "engine.llamafile", "bytes")
	preferred := filepath.Join(dir, "lib", "engines", "payload")
	fallback := filepath.Join(dir, "home", ".ollama", "engines", "payload")

	orig := ensureWritable
	defer func() { ensureWritable = orig }()
	ensureWritable = func(root string) error {
		if root == preferred {
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			return errors.New("permission denied")
		}
		return orig(root)
	}

	hashed := 0
	defer func(orig func(string, payloadOwner) string) { digestOf = orig }(digestOf)
	digestOf = func(artifact string, p payloadOwner) string {
		hashed++
		return digestArtifact(artifact)
	}

	if got := PreparePayloadHome(art, preferred, fallback); got != fallback {
		t.Fatalf("PreparePayloadHome() = %q, want the fallback %q", got, fallback)
	}
	if _, err := os.Stat(filepath.Join(preferred, payloadMarker)); !os.IsNotExist(err) {
		t.Errorf("wrote into a root we cannot write: %v", err)
	}
	if hashed != 1 {
		t.Errorf("hashed the artifact %d times for 2 roots; an unusable root must be rejected before any work", hashed)
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
