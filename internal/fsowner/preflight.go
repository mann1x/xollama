//go:build !windows

package fsowner

import (
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// Preflight reports a shared installation this process is going to damage.
//
// Adoption covers the case where xollama runs AS ROOT: it can chown, so it
// does. The case it cannot fix is the mirror image -- an ordinary user running
// xollama against a store that belongs to a system service. chown(2) is
// privileged, so nothing can be handed over, and every file this process
// creates will be one the service cannot replace. The result is the same
// silent failure either way: a model list that takes seconds instead of
// milliseconds, reported by clients as a timeout or a 404.
//
// There is no safe automatic answer to that, so it is said out loud, once, with
// the command that fixes it. It never blocks startup: the person may well be
// pointing xollama at a store deliberately, and a warning they can act on beats
// a refusal they have to work around.
func Preflight(paths ...string) {
	if runningAsRoot() {
		return // root adopts instead; nothing to warn about
	}
	me := Owner{UID: os.Geteuid(), GID: os.Getegid()}
	for _, path := range paths {
		if path == "" {
			continue
		}
		owner, ok := ownerOf(path)
		if !ok || owner == me || owner.UID == me.UID {
			continue
		}
		if canWrite(path) {
			// Group-writable and we are in the group: files we add will carry
			// our uid but the service can still replace them.
			continue
		}
		slog.Warn("this directory belongs to another account and xollama cannot write it",
			"path", path,
			"owner", describe(owner),
			"running_as", describe(me),
			"consequence", "models and caches written here will be unusable by whichever account owns the installation, which surfaces as a slow model list rather than an error",
			"fix", fmt.Sprintf("run xollama as %s, or point OLLAMA_MODELS somewhere this account owns", describe(owner)))
	}
}

func canWrite(path string) bool {
	return syscall.Access(path, 2 /* W_OK */) == nil
}

// describe prefers a name, because "990" is not something anyone can act on.
func describe(o Owner) string {
	name := strconv.Itoa(o.UID)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	if g, err := user.LookupGroupId(strconv.Itoa(o.GID)); err == nil {
		return name + ":" + g.Name
	}
	return name
}
