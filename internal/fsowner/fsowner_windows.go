package fsowner

// Windows has no equivalent problem to solve here. Ollama installs per user,
// under %LOCALAPPDATA%, and there is no unprivileged service account owning the
// model store for an elevated process to lock out. Every entry point reports
// "nothing to do" so callers need no build tags of their own.

var runningAsRoot = func() bool { return false }

func ownerOf(string) (Owner, bool) { return Owner{}, false }

func chown(string, Owner) error { return nil }

func namedServiceAccount() (Owner, bool) { return Owner{}, false }
