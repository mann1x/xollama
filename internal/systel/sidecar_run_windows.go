package systel

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sidecarExe is the GPU metrics sidecar the installer lays down beside
// xollama.exe (tools/gpu-sidecar).
const sidecarExe = "xollama-gpu-sidecar.exe"

const (
	// sidecarInterval is how often the sidecar writes: the snapshot's own
	// refresh, well inside sidecarMaxAge.
	sidecarInterval = 2
	// sidecarRestartAfter is how long a sidecar that exited on its own waits
	// before it is started again.
	sidecarRestartAfter = 30 * time.Second
	createNoWindow      = 0x08000000
)

// sidecarRunner runs the sidecar while the snapshot is polled. It is the one
// process this package starts: no admin, no driver (--no-cpu keeps it off the
// Ryzen SMU), no window, bound to the server by a kill-on-close job and by
// --parent, and stopped when the polling stops.
type sidecarRunner struct {
	exe string // empty when the server's own location is unknown
	out string

	mu      sync.Mutex
	cmd     *exec.Cmd     // nil when no sidecar is running or starting
	process *os.Process   // cmd's, once it has started
	done    chan struct{} // closed when cmd has exited
	started time.Time
	job     windows.Handle
}

func newSidecarRunner(out string) *sidecarRunner {
	if out == "" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	return &sidecarRunner{exe: filepath.Join(filepath.Dir(self), sidecarExe), out: out}
}

func (r *sidecarRunner) ensure(fresh bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		select {
		case <-r.done:
			r.cmd = nil
		default:
			return
		}
	}
	if !fresh && time.Since(r.started) < sidecarRestartAfter {
		return
	}
	// Not installed is the ordinary state of a build run from a source tree.
	if _, err := os.Stat(r.exe); err != nil {
		return
	}
	r.started = time.Now()
	// A file left by an earlier run is stale, and a stale file is warned;
	// none at all is the quiet state until the first write.
	_ = os.Remove(r.out)
	cmd := exec.Command(r.exe,
		"--out", r.out,
		"--interval", strconv.Itoa(sidecarInterval),
		"--no-cpu",
		"--parent", strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	tail := &tailWriter{max: 2048}
	cmd.Stderr = tail
	done := make(chan struct{})
	r.cmd, r.process, r.done = cmd, nil, done
	// Started off the caller's path: ensure runs inside a poll, and a poll
	// never waits on anything.
	go r.run(cmd, done, tail)
}

func (r *sidecarRunner) run(cmd *exec.Cmd, done chan struct{}, tail *tailWriter) {
	defer close(done)
	if err := cmd.Start(); err != nil {
		slog.Warn("GPU metrics sidecar did not start", "path", r.exe, "error", err)
		return
	}
	if err := r.adopt(cmd.Process.Pid); err != nil {
		slog.Debug("GPU metrics sidecar runs without a kill-on-close job; --parent still ends it", "error", err)
	}
	r.mu.Lock()
	current := r.cmd == cmd
	if current {
		r.process = cmd.Process
	}
	r.mu.Unlock()
	if !current { // stopped while it was starting
		_ = cmd.Process.Kill()
	} else {
		slog.Debug("GPU metrics sidecar started", "pid", cmd.Process.Pid, "out", r.out)
	}
	err := cmd.Wait()
	r.mu.Lock()
	current = r.cmd == cmd
	r.mu.Unlock()
	// One stop ended is not news; one that ended by itself is.
	if current {
		slog.Warn("GPU metrics sidecar exited", "error", err, "stderr", tail.String(),
			"restart_after", sidecarRestartAfter)
	}
}

func (r *sidecarRunner) stop() {
	r.mu.Lock()
	cmd, process, done := r.cmd, r.process, r.done
	r.cmd, r.process = nil, nil
	r.mu.Unlock()
	if cmd != nil {
		// Not started yet: run kills it the moment it is, since it is no
		// longer the current one.
		if process != nil {
			_ = process.Kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	_ = os.Remove(r.out)
}

// adopt puts the sidecar in a job that ends it when the server's handle on the
// job closes, which happens however the server exits.
func (r *sidecarRunner) adopt(pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job == 0 {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return err
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.CloseHandle(job)
			return err
		}
		r.job = job
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(p)
	return windows.AssignProcessToJobObject(r.job, p)
}

// tailWriter keeps the last max bytes written to it: what the sidecar said
// before it exited.
type tailWriter struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	if len(w.b) > w.max {
		w.b = w.b[len(w.b)-w.max:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.b))
}
