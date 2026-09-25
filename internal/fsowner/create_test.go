//go:build !windows

package fsowner

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	svcUID = 4242
	svcGID = 4243
)

// serviceOwnedStore fakes a model store belonging to a system service, which is
// what a packaged Linux install looks like.
func serviceOwnedStore(t *testing.T) string {
	t.Helper()
	needRealRoot(t)
	store := filepath.Join(t.TempDir(), "models")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(store, svcUID, svcGID); err != nil {
		t.Fatal(err)
	}
	return store
}

func assertOwnedByService(t *testing.T, path string) {
	t.Helper()
	got := ownerMust(t, path)
	if got.UID != svcUID {
		t.Errorf("%s is owned by %v; the service (uid %d) cannot replace it", path, got, svcUID)
	}
}

// TestForFollowsTheNearestExistingAncestor is the rule the wrappers run on: a
// new file belongs to whoever owns the tree it lands in. Nothing here knows
// which directories are "the store"; the filesystem is the configuration.
func TestForFollowsTheNearestExistingAncestor(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	// Several levels deeper than anything that exists yet.
	got, ok := For(filepath.Join(store, "blobs", "partial", "sha256-abc"))
	if !ok {
		t.Fatal("For() found no owner below a service-owned directory")
	}
	if got.UID != svcUID {
		t.Errorf("For() = %v, want uid %d", got, svcUID)
	}
}

// TestARootOwnedAncestorIsNotEvidence: /usr/local/lib/ollama is root-owned on
// every packaged install, and so is a store a previous root run already
// spoiled. Believing either would make the bug permanent.
func TestARootOwnedAncestorIsNotEvidence(t *testing.T) {
	asRoot(t, true)
	root := t.TempDir() // owned by root, since these tests run as root

	got, ok := For(filepath.Join(root, "engines", "payload"))
	if ok && got.UID == 0 {
		t.Error("For() adopted root from a root-owned ancestor")
	}
}

// TestMkdirAllHandsOverEveryLevelItCreates. A pull creates blobs/ and
// partial/ in one call; if only the leaf were adopted, the service would be
// locked out one level up and the failure would look identical.
func TestMkdirAllHandsOverEveryLevelItCreates(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	deep := filepath.Join(store, "blobs", "partial", "work")
	if err := MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(store, "blobs"),
		filepath.Join(store, "blobs", "partial"),
		deep,
	} {
		assertOwnedByService(t, p)
	}
}

// TestMkdirAllLeavesExistingDirectoriesAlone. Taking ownership of a directory
// somebody else made, just because we wrote inside it, is not ours to do.
func TestMkdirAllLeavesExistingDirectoriesAlone(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	existing := filepath.Join(store, "preexisting")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(existing, 5555, 5556); err != nil {
		t.Fatal(err)
	}

	if err := MkdirAll(filepath.Join(existing, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ownerMust(t, existing); got.UID != 5555 {
		t.Errorf("an existing directory was re-owned to %v", got)
	}
}

// TestTheMetadataCacheIsWritable is the actual incident, in miniature: the
// parsed-GGUF cache is written as a temp file and renamed, and rename does not
// change ownership -- so it has to be right at CreateTemp.
func TestTheMetadataCacheIsWritable(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	dir := filepath.Join(store, "metadata")
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := CreateTemp(dir, ".gguf-metadata-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	final := filepath.Join(dir, "sha256-abc.json")
	if err := os.Rename(tmp.Name(), final); err != nil {
		t.Fatal(err)
	}
	assertOwnedByService(t, final)
}

func TestWriteFileAndCreateHandOver(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	written := filepath.Join(store, "written.json")
	if err := WriteFile(written, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertOwnedByService(t, written)

	created := filepath.Join(store, "created.bin")
	f, err := Create(created)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	assertOwnedByService(t, created)
}

// OpenFile must only adopt when it could have created the file -- reopening
// somebody else's file for append is not a reason to take it.
func TestOpenFileOnlyAdoptsWhatItMayHaveCreated(t *testing.T) {
	asRoot(t, true)
	store := serviceOwnedStore(t)

	path := filepath.Join(store, "log.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 5555, 5556); err != nil {
		t.Fatal(err)
	}

	f, err := OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := ownerMust(t, path); got.UID != 5555 {
		t.Errorf("OpenFile re-owned an existing file to %v", got)
	}
}

// TestAnUnprivilegedProcessIsUntouched. The wrappers run on every pull for
// every user; when there is no decision to make they must be os plus a stat,
// and above all must not fail.
func TestAnUnprivilegedProcessIsUntouched(t *testing.T) {
	asRoot(t, false)
	dir := t.TempDir()

	if err := MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(dir, "a", "b", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := For(filepath.Join(dir, "a", "b", "f")); ok {
		t.Error("For() offered an owner to a non-root process, which cannot chown anyway")
	}
}

// TestWrappersReportTheirOwnErrors: the ownership step must never swallow or
// invent a failure of the underlying os call.
func TestWrappersReportTheirOwnErrors(t *testing.T) {
	asRoot(t, true)
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(filepath.Join(blocked, "under"), 0o755); err == nil {
		t.Error("MkdirAll() = nil, want the os error")
	}
	if err := WriteFile(filepath.Join(blocked, "under", "f"), nil, 0o644); err == nil {
		t.Error("WriteFile() = nil, want the os error")
	}
	if _, err := Create(filepath.Join(blocked, "under", "f")); err == nil {
		t.Error("Create() = nil, want the os error")
	}
}
