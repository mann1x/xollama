package llm

import (
	"strings"
	"testing"
)

func TestACommitLimitRefusalReachesTheClient(t *testing.T) {
	refused := "srv  load_model: host commit [load]: only 722 MiB of the system's commit limit is left (64000 of 64722 MiB committed, RAM + pagefile; margin 4096 MiB) — Lower -c or -np"
	var w StatusWriter
	if _, err := w.Write([]byte(refused + "\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.LastError(), "host commit [load]: only 722 MiB") {
		t.Fatalf("LastError = %q", w.LastError())
	}
}

func TestAHealthyBootsCommitLineIsNotAnError(t *testing.T) {
	healthy := "srv  load_model: host commit [load]: the system has committed 30000 of 64000 MiB (RAM + pagefile), 34000 MiB left, 20000 MiB of RAM free"
	var w StatusWriter
	if _, err := w.Write([]byte(healthy + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := w.LastError(); got != "" {
		t.Fatalf("LastError = %q", got)
	}
}
