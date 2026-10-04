package engine

// xollama: an engine never outlives the server that started it (eleven2go,
// 2026-10-04). Additive; called from the two places an engine is started.
//
// Windows does not end a child with its parent. A server that is killed, or
// crashes, left its engine running with the model loaded, and on an AMD
// discrete card through Vulkan that idle orphan hung the display driver within
// 14 to 22 seconds, twice out of twice: a watchdog dump 0x141, an engine that
// could no longer be killed, once the card gone until a reboot and once the
// whole machine frozen. So every engine goes into one job object whose only
// handle this process holds, with kill-on-close: when the server is gone, by
// any road, the kernel ends what is in the job.

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// serverJob is the job every engine of this server belongs to. Its handle is
// never closed and never inherited: the process ending is what closes it.
var serverJob = sync.OnceValues(func() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("set kill-on-close: %w", err)
	}
	return job, nil
})

// BindLifetime ties a started engine to this server: it ends when the server
// does. A failure is returned for the caller to say, never fatal to the load.
func BindLifetime(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	job, err := serverJob()
	if err != nil {
		return err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open engine process %d: %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("assign engine process %d to the server's job: %w", cmd.Process.Pid, err)
	}
	return nil
}
