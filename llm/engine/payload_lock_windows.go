package engine

import (
	"os"

	"golang.org/x/sys/windows"

	"github.com/ollama/ollama/internal/fsowner"
)

// lockPayloadRoot takes an exclusive LockFileEx on path, waiting for another
// process that holds it, and returns the release.
func lockPayloadRoot(path string) (func(), error) {
	f, err := fsowner.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}, nil
}
