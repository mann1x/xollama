package manifest

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"syscall"
)

// A model whose manifest cannot be READ still owns its blobs.
//
// Pruning works by subtraction: every blob some named manifest lists is
// kept, and a candidate nobody lists is removed. Upstream skips a manifest it
// cannot read and goes on, which is right for one that is damaged for good
// and wrong for one the system would not open: its layers then count as
// nobody's, and the next prune that names them deletes them.
//
// That is how a model lost its weights on Windows (2026-10-09, upstream
// v0.40.0). Named manifests had become symbolic links, the machine refused
// to follow them ("The path cannot be traversed because it contains an
// untrusted mount point"), and `xollama tweak model` rewrote the model: the
// new manifest was written, could not be read back, and the prune of the old
// manifest's layers removed the 16 GB of weights the new one still listed.
// Upstream v0.40.1 stores copies on Windows, which removes that cause; this
// removes the mechanism, for whatever makes a manifest unreadable next.
//
// So an I/O failure on a named manifest stops the prune: nothing is removed,
// and the blobs wait for a prune that can see every manifest. A manifest that
// is missing behind its name, or whose content is not a manifest, is skipped
// as before -- there is nothing in it to protect, and it must not hold the
// store's garbage forever.
//
// xollama-hook: prune-guard

// errManifestUnreadable marks a prune that could not see every manifest.
var errManifestUnreadable = errors.New("a named manifest could not be read")

// unreadableManifest reports whether reading a named manifest failed in the
// file system, as opposed to finding nothing or finding something that is
// not a manifest.
func unreadableManifest(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var errno syscall.Errno
	return errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &errno)
}

// pruneRefused reports whether the retained set could not be established,
// and says so once per prune.
func pruneRefused(err error) bool {
	if !errors.Is(err, errManifestUnreadable) {
		return false
	}
	slog.Warn("not pruning blobs: a model's manifest could not be read, so its blobs cannot be told from unused ones", "error", err)
	return true
}
