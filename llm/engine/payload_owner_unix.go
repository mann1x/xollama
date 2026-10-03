//go:build !windows

package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
)

// The engine writes into the HOME it is given as whoever it runs as: cosmo's
// dlopen helper in .cosmo, the NVIDIA driver's cache in .nv, audio.cpp's data
// in .cache. xollama creates none of those, so internal/fsowner never sees
// them. A server run once as root (an administrator's `sudo xollama serve`
// beside a service that runs as `ollama`) therefore left root-owned files
// there, and the service's next engine could not use the helper: every GPU
// load failed with "dlopen() isn't supported on this platform" while device
// listing, run with another HOME, still showed the GPU (solidPC, 2026-10-03).

// AdoptPayloadHome hands everything under an engine HOME to the account the
// service runs as. Only root has anything to do, and it is never fatal: it is
// a correctness measure for the next process, not for this one.
//
// It acts only on a directory PreparePayloadHome made the engine's: one
// holding the marker. A launch that fell back to the inherited HOME passes the
// operator's own home directory here, and that is not ours to hand to anyone.
func AdoptPayloadHome(home string) {
	if home == "" {
		return
	}
	if _, err := os.Lstat(filepath.Join(home, payloadMarker)); err != nil {
		return
	}
	if owner, ok := fsowner.Intended(envconfig.Models()); ok {
		adoptPayloadTree(home, owner)
	}
}

// adoptPayloadTree gives every directory and regular file under home to o. A
// symlink is left alone: chown follows it, and what it names may be outside
// the tree.
func adoptPayloadTree(home string, o fsowner.Owner) {
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		// An entry that cannot be read is skipped, not a reason to stop: the
		// rest of the tree is still worth handing over.
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			fsowner.AdoptQuietly(path, o)
		}
		return nil
	})
}

// payloadForeign reports the first entry under root that another account left
// and this one cannot use: a directory it cannot read, write and enter, or a
// file it cannot read. Root is never stopped by a mode bit and has nothing to
// find. It is a variable so a test can stand in for another account.
var payloadForeign = func(root string) error {
	uid := os.Getuid()
	if uid == 0 {
		return nil
	}
	var found error
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			found = fmt.Errorf("%s cannot be read by this account (uid %d): %w", path, uid, err)
			return filepath.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) == uid {
			return nil
		}
		const read, write, enter = 4, 2, 1
		need := uint32(read)
		if d.IsDir() {
			need = read | write | enter
		}
		if err := syscall.Access(path, need); err != nil {
			found = fmt.Errorf("%s belongs to uid %d and this account (uid %d) cannot use it", path, st.Uid, uid)
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
