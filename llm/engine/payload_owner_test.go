//go:build !windows

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ollama/ollama/internal/fsowner"
)

// service is an account that is neither root nor anyone on the host.
var service = fsowner.Owner{UID: 54321, GID: 54321}

func uidOf(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Sys().(*syscall.Stat_t).Uid)
}

// engineLeftovers builds what a root-run engine leaves in its HOME: cosmo's
// helper with a 0600 source, the NVIDIA cache in a 0700 directory, and a link
// out of the tree.
func engineLeftovers(t *testing.T, home string) (inside []string, outside string) {
	t.Helper()
	outside = filepath.Join(t.TempDir(), "not-ours")
	for path, mode := range map[string]os.FileMode{
		filepath.Join(home, ".cosmo", "dlopen-helper.c"): 0o600,
		filepath.Join(home, ".cosmo", "dlopen-helper"):   0o755,
		filepath.Join(home, ".nv", "ComputeCache", "x"):  0o600,
		outside: 0o600,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if path != outside {
			inside = append(inside, path, filepath.Dir(path))
		}
	}
	if err := os.Symlink(outside, filepath.Join(home, ".cosmo", "link")); err != nil {
		t.Fatal(err)
	}
	return inside, outside
}

func TestARootRunHandsTheEnginesFilesToTheService(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can give a file away")
	}
	home := t.TempDir()
	inside, outside := engineLeftovers(t, home)

	adoptPayloadTree(home, service)

	for _, path := range inside {
		if got := uidOf(t, path); got != service.UID {
			t.Errorf("%s belongs to uid %d, want the service's %d", path, got, service.UID)
		}
	}
	// A directory the engine made 0700 must be one the service can purge.
	if fi, _ := os.Stat(filepath.Join(home, ".nv")); fi.Mode().Perm()&0o070 != 0o070 || fi.Mode()&os.ModeSetgid == 0 {
		t.Errorf(".nv is %v, want group rwx and setgid", fi.Mode())
	}
	if got := uidOf(t, outside); got != 0 {
		t.Errorf("the file a link points at outside the tree was given to uid %d", got)
	}
}

// A launch that found no usable root runs the engine with the inherited HOME.
// That is the operator's home directory, and adoption must not touch it.
func TestOnlyADirectoryXollamaMadeTheEnginesIsHandedOver(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can give a file away")
	}
	store := t.TempDir()
	if err := os.Chown(store, service.UID, service.GID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_MODELS", store)

	home := t.TempDir()
	inside, _ := engineLeftovers(t, home)
	AdoptPayloadHome(home)
	for _, path := range inside {
		if got := uidOf(t, path); got != 0 {
			t.Fatalf("%s was given to uid %d though the directory has no payload marker", path, got)
		}
	}

	if err := os.WriteFile(filepath.Join(home, payloadMarker), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	AdoptPayloadHome(home)
	for _, path := range inside {
		if got := uidOf(t, path); got != service.UID {
			t.Errorf("%s belongs to uid %d, want the store owner's %d", path, got, service.UID)
		}
	}
}

func TestARootHoldingFilesThisAccountCannotUseFallsBackToTheNext(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "engine")
	if err := os.WriteFile(artifact, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	spoiled, clean := filepath.Join(dir, "spoiled"), filepath.Join(dir, "clean")

	orig := payloadForeign
	defer func() { payloadForeign = orig }()
	payloadForeign = func(root string) error {
		if root == spoiled {
			return errors.New(root + "/.cosmo belongs to uid 0 and this account (uid 990) cannot use it")
		}
		return nil
	}

	if got := PreparePayloadHome(artifact, spoiled, clean); got != clean {
		t.Fatalf("PreparePayloadHome = %q, want the next root %q", got, clean)
	}
	// Nothing was prepared in a root the engine could not work in.
	if _, err := os.Stat(filepath.Join(spoiled, payloadMarker)); err == nil {
		t.Fatal("the spoiled root was marked as the engine's")
	}
}

func TestHomeOfIsTheLastOne(t *testing.T) {
	if got := HomeOf([]string{"HOME=/root", "PATH=/bin", "HOME=/lib/engines/payload"}); got != "/lib/engines/payload" {
		t.Fatalf("HomeOf = %q", got)
	}
	if got := HomeOf([]string{"PATH=/bin"}); got != "" {
		t.Fatalf("HomeOf = %q, want none", got)
	}
}
