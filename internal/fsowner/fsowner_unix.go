//go:build !windows

package fsowner

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// serviceAccounts are the names a packaged install runs under, best first.
var serviceAccounts = []string{"ollama"}

// a var so a test can exercise both sides of the decision on one machine
var runningAsRoot = func() bool { return os.Geteuid() == 0 }

func ownerOf(path string) (Owner, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Owner{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, false
	}
	return Owner{UID: int(st.Uid), GID: int(st.Gid)}, true
}

func chown(path string, o Owner) error { return os.Chown(path, o.UID, o.GID) }

func namedServiceAccount() (Owner, bool) {
	for _, name := range serviceAccounts {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			continue
		}
		return Owner{UID: uid, GID: gid}, true
	}
	return Owner{}, false
}
