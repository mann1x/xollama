//go:build !windows

package fsowner

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func asRoot(t *testing.T, v bool) {
	t.Helper()
	orig := runningAsRoot
	runningAsRoot = func() bool { return v }
	t.Cleanup(func() { runningAsRoot = orig })
}

// needRealRoot skips a test that cannot be faked: chown(2) is refused to
// everyone but root, so on a developer laptop these assertions are unavailable
// and a skip is more honest than a weaker test.
func needRealRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs real root: chown(2) is privileged")
	}
}

func ownerMust(t *testing.T, path string) Owner {
	t.Helper()
	o, ok := ownerOf(path)
	if !ok {
		t.Fatalf("ownerOf(%s) failed", path)
	}
	return o
}

// TestNothingToDoWhenNotRoot is the common case and the one that must stay
// cheap: an unprivileged server already creates files as the right user, and
// trying to chown them would fail on every single call.
func TestNothingToDoWhenNotRoot(t *testing.T) {
	asRoot(t, false)
	if o, ok := Intended(t.TempDir()); ok {
		t.Errorf("Intended() = %v, true; a non-root process has no ownership decision to make", o)
	}
}

// TestIntendedFollowsTheModelStore: the store is the evidence. Whoever owns it
// must be able to write it, so that identity IS the service -- no name lookup,
// no configuration, no guess.
func TestIntendedFollowsTheModelStore(t *testing.T) {
	needRealRoot(t)
	asRoot(t, true)

	store := t.TempDir()
	if err := os.Chown(store, 4242, 4243); err != nil {
		t.Fatal(err)
	}

	got, ok := Intended(store)
	if !ok {
		t.Fatal("Intended() returned false for a store owned by a service account")
	}
	if want := (Owner{UID: 4242, GID: 4243}); got != want {
		t.Errorf("Intended() = %v, want %v", got, want)
	}
}

// A root-owned store tells us nothing -- it is the state a previous root run
// leaves behind, so believing it would make the bug permanent.
func TestARootOwnedStoreIsNotEvidence(t *testing.T) {
	needRealRoot(t)
	asRoot(t, true)

	store := t.TempDir() // owned by root, since the test runs as root
	got, ok := Intended(store)
	if ok && got.UID == 0 {
		t.Error("Intended() adopted root from a root-owned store; that is the state the bug leaves behind, not a decision")
	}
}

// TestAdoptHandsOverAndOpensTheGroup is the fix itself. The directory must end
// up owned by the service AND group-writable with setgid, because a purge
// removes files by writing the directory -- so a service-group directory stays
// purgeable even when it holds entries an earlier root run created.
func TestAdoptHandsOverAndOpensTheGroup(t *testing.T) {
	needRealRoot(t)

	dir := filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := Owner{UID: 4242, GID: 4243}
	if err := Adopt(dir, want); err != nil {
		t.Fatal(err)
	}

	if got := ownerMust(t, dir); got != want {
		t.Errorf("owner = %v, want %v", got, want)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o070 != 0o070 {
		t.Errorf("mode = %o, want the group rwx so the service can purge it", mode)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Error("setgid missing; subdirectories would not inherit the service group")
	}
}

// TestAdoptOnAFileLeavesTheModeAlone: the marker is data, not a directory, and
// widening its mode would be a change nobody asked for.
func TestAdoptOnAFileLeavesTheModeAlone(t *testing.T) {
	needRealRoot(t)

	path := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Adopt(path, Owner{UID: 4242, GID: 4243}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 0644 unchanged", got)
	}
	if got := ownerMust(t, path); got.UID != 4242 {
		t.Errorf("owner = %v, want uid 4242", got)
	}
}

// TestAdoptNeverFailsTheCaller: AdoptQuietly exists because ownership is a
// correctness measure for the NEXT process and never a reason to refuse this
// one. A path that does not exist must not panic or block a launch.
func TestAdoptNeverFailsTheCaller(t *testing.T) {
	AdoptQuietly(filepath.Join(t.TempDir(), "absent"), Owner{UID: 4242, GID: 4243})
}

// Guard against the syscall assumption silently changing under us.
func TestOwnerOfReadsTheRealIdentity(t *testing.T) {
	dir := t.TempDir()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if got := ownerMust(t, dir); got.UID != int(st.Uid) || got.GID != int(st.Gid) {
		t.Errorf("ownerOf() = %v, want %d:%d", got, st.Uid, st.Gid)
	}
}
