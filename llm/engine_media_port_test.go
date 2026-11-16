package llm

import (
	"net"
	"testing"
)

// bug-352: the kernel handed out a port Docker's readarr served on every
// address, and the engine's /health was read from readarr.
func TestTheMediaPortIsNotOneSomethingAnswersOn(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	busy := taken.Addr().(*net.TCPAddr).Port

	free := loopbackPort()
	calls := 0
	pickLoopbackPort = func() int {
		calls++
		if calls == 1 {
			return busy // what the kernel handed out on solidPC
		}
		return free
	}
	t.Cleanup(func() { pickLoopbackPort = loopbackPort })

	if got := freePort(); got != free {
		t.Fatalf("freePort = %d, want %d (the port nothing answers on, not %d)", got, free, busy)
	}
	if calls != 2 {
		t.Fatalf("picked %d times, want 2", calls)
	}
}
