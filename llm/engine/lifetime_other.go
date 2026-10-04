//go:build !windows

package engine

import "os/exec"

// BindLifetime ties a started engine to this server on Windows
// (lifetime_windows.go). Elsewhere it does nothing.
func BindLifetime(cmd *exec.Cmd) error { return nil }
