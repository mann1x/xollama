package fsowner

import (
	"os"
	"path/filepath"
)

// The wrappers below are drop-in replacements for the os functions of the same
// name. Each creates what it was asked for and then, if the process is root and
// the destination belongs to a service account, hands the new path over.
//
// They are safe to call for ANY path, which is the point: the decision is made
// from the destination, not from the call site. A path outside a shared
// installation has no service owner to find, so the wrapper is os.Whatever plus
// one stat.

// For returns the identity a newly created path should belong to.
//
// The rule is the one a person would apply: a new file belongs to whoever owns
// the directory tree it is going into. So walk up to the nearest directory that
// already exists and read its owner.
//
// Two refinements, both learned the hard way:
//
//   - A root-owned ancestor is not evidence. `/usr/local/lib/ollama` is
//     root-owned on every packaged install, and so is a model store that a
//     previous root command already spoiled -- believing either would make the
//     bug permanent. In that case fall back to the named service account.
//   - A non-root process gets no answer at all. It already creates files as
//     itself, and chown(2) would fail anyway.
func For(path string) (Owner, bool) {
	if !runningAsRoot() {
		return Owner{}, false
	}
	for dir := filepath.Dir(path); ; {
		if o, ok := ownerOf(dir); ok {
			if o.UID != 0 {
				return o, true
			}
			break // exists, but root-owned: not evidence
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return namedServiceAccount()
}

// MkdirAll is os.MkdirAll, giving every directory it creates to the owner of
// the tree. Only directories that did not exist are touched -- an existing
// directory belongs to whoever made it.
func MkdirAll(path string, perm os.FileMode) error {
	// Read the owner BEFORE creating anything. Afterwards the nearest existing
	// ancestor of path is a directory this call just made as root, so For()
	// would be asking itself what it should have done.
	missing := missingAncestors(path)
	var (
		owner Owner
		known bool
	)
	if len(missing) > 0 {
		// missingAncestors is deepest first, so the last entry is the
		// shallowest one -- and its parent is the deepest directory that
		// already exists, which is the evidence we want.
		owner, known = For(missing[len(missing)-1])
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	if known {
		for _, p := range missing {
			AdoptQuietly(p, owner)
		}
	}
	return nil
}

// WriteFile is os.WriteFile plus the handover.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(name, data, perm); err != nil {
		return err
	}
	adopt(name)
	return nil
}

// Create is os.Create plus the handover. The file is adopted immediately, while
// it is still empty, so a caller that never closes it cleanly still leaves
// something the service can replace.
func Create(name string) (*os.File, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	adopt(name)
	return f, nil
}

// OpenFile is os.OpenFile plus the handover, applied only when the call could
// have created the file.
func OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 {
		adopt(name)
	}
	return f, nil
}

// CreateTemp is os.CreateTemp plus the handover. Blobs arrive as temp files and
// are renamed into place, so the ownership has to be right here -- a rename
// does not change it.
func CreateTemp(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	adopt(f.Name())
	return f, nil
}

// Adopted hands an already-created path over. For code that cannot use the
// wrappers -- something that shells out, or writes through a third-party API.
func Adopted(path string) { adopt(path) }

func adopt(path string) {
	if o, ok := For(path); ok {
		AdoptQuietly(path, o)
	}
}

// missingAncestors lists path and every parent of it that does not yet exist,
// deepest last, so MkdirAll can hand over exactly what it created and nothing
// that was already there.
func missingAncestors(path string) []string {
	var missing []string
	for p := filepath.Clean(path); ; {
		if _, err := os.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return missing
}
