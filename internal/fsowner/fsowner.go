// Package fsowner decides who should own the files xollama creates on a
// multi-user install, and hands them over.
//
// The problem is specific and it has already cost this project a day. Ollama on
// Linux is a system service: the package installs it, systemd runs it as the
// unprivileged `ollama` user, and the model store belongs to that user. An
// administrator then runs a command as root -- a pull, a one-off serve, a
// benchmark -- and every file that command creates is owned by root. The
// service cannot read or replace them afterwards.
//
// Nothing about that failure looks like a permission error. The measured
// symptom was a model list that took 9.89 s instead of 0.06 s, because the
// parsed-GGUF metadata cache had 81 root-owned 0600 entries the service could
// not open, so it re-parsed every GGUF header on every request. Clients
// reported it as a timeout, or as a 404 for a model that was plainly there.
//
// So when xollama runs as root it does not create files as root. It finds the
// identity the installation actually belongs to and gives them to it.
package fsowner

import (
	"fmt"
	"os"
)

// Owner is a uid/gid pair. It is deliberately not a username: the only thing
// that matters at the syscall is the numeric identity, and a name that does not
// resolve is worse than no answer.
type Owner struct {
	UID int
	GID int
}

func (o Owner) String() string { return fmt.Sprintf("%d:%d", o.UID, o.GID) }

// Intended returns the identity new files should belong to, and whether there
// is anything to do at all.
//
// It reports false in the ordinary case, which is the whole point: a process
// that is not root already creates files as the right user, and chowning them
// would be both pointless and impossible. Only root has a decision to make.
//
// modelsDir is the evidence. The model store is the one directory whose
// ownership is not a matter of taste -- the service must be able to write it,
// so whoever owns it IS the service. Falling back to a named account is a
// second-best guess for a first run where no store exists yet.
func Intended(modelsDir string) (Owner, bool) {
	if !runningAsRoot() {
		return Owner{}, false
	}
	if o, ok := ownerOf(modelsDir); ok && o.UID != 0 {
		return o, true
	}
	return namedServiceAccount()
}

// Adopt gives path to o, unless it already belongs to them.
//
// Directories additionally get the setgid bit and group write. That is not
// decoration: a purge removes files by writing the DIRECTORY, not the files, so
// a directory group-owned by the service stays purgeable even when it holds
// entries some earlier root invocation created. New subdirectories inherit the
// group for the same reason.
func Adopt(path string, o Owner) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if cur, ok := ownerOf(path); ok && cur == o {
		if !fi.IsDir() {
			return nil
		}
	}
	if err := chown(path, o); err != nil {
		return err
	}
	if fi.IsDir() {
		// os.ModeSetgid, not a raw 0o2000 bit: os.Chmod takes an os.FileMode,
		// where setgid lives in the high bits, and a bare 0o2775 quietly
		// becomes 0o775.
		return os.Chmod(path, 0o775|os.ModeSetgid)
	}
	return nil
}

// AdoptQuietly is Adopt for callers that must not fail over it. Ownership is a
// correctness measure for the NEXT process, never a reason to refuse this one.
func AdoptQuietly(path string, o Owner) {
	_ = Adopt(path, o)
}
