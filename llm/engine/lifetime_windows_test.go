package engine

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test binary stands in for both processes. As "server" it starts a
// "sleeper", binds it or not, prints its pid and waits to be killed.
const lifetimeRole = "XOLLAMA_LIFETIME_TEST_ROLE"

func TestMain(m *testing.M) {
	switch os.Getenv(lifetimeRole) {
	case "sleeper":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "server-bound", "server-unbound":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), lifetimeRole+"=sleeper")
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		if os.Getenv(lifetimeRole) == "server-bound" {
			if err := BindLifetime(child); err != nil {
				fmt.Println("error", err)
				os.Exit(1)
			}
		}
		fmt.Println("pid", child.Process.Pid)
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// killedServersChild starts a server in the given role, kills it the way a
// crash or a task manager does, and returns its child's pid.
func killedServersChild(t *testing.T, role string) int {
	t.Helper()
	server := exec.Command(os.Args[0])
	server.Env = append(os.Environ(), lifetimeRole+"="+role)
	out, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("server said nothing: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid ")))
	if err != nil {
		t.Fatalf("server said %q", line)
	}
	if !alive(pid) {
		t.Fatalf("child %d is not running before the server is killed", pid)
	}
	if err := server.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = server.Wait()
	return pid
}

func TestAnEngineEndsWithAKilledServer(t *testing.T) {
	pid := killedServersChild(t, "server-bound")
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
			t.Fatalf("engine %d outlived its killed server by 10 s", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The control: without the binding Windows leaves the child running, which is
// the orphan the binding exists for. If this ever fails the test above proves
// nothing.
func TestAnUnboundChildOutlivesAKilledServer(t *testing.T) {
	pid := killedServersChild(t, "server-unbound")
	p, err := os.FindProcess(pid)
	if err == nil {
		defer p.Kill()
	}
	time.Sleep(2 * time.Second)
	if !alive(pid) {
		t.Fatalf("child %d ended with its server without the binding", pid)
	}
}
