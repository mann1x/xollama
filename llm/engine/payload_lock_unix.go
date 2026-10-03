//go:build !windows

package engine

import (
	"os"
	"syscall"

	"github.com/ollama/ollama/internal/fsowner"
)

// lockPayloadRoot takes an exclusive flock on path, waiting for another
// process that holds it, and returns the release.
func lockPayloadRoot(path string) (func(), error) {
	f, err := fsowner.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
