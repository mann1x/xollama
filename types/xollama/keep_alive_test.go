package xollama_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// A model's keep_alive means what the same value means in a request.
func TestKeepAliveReadsAsARequestDoes(t *testing.T) {
	for _, v := range []string{"30m", "1h", "0", "0s", "-1", "-5m", "90", "1.5", "2h30m"} {
		var want api.Duration
		raw := `"` + v + `"`
		if _, err := json.Number(v).Float64(); err == nil {
			raw = v // upstream reads a bare number as seconds
		}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatalf("%s: upstream refuses it: %v", v, err)
		}
		got, ok, err := xollama.ParseKeepAlive(v)
		if err != nil || !ok {
			t.Fatalf("%s: ok %v err %v", v, ok, err)
		}
		if got != want.Duration {
			t.Errorf("%s: %v, a request reads %v", v, got, want.Duration)
		}
	}
	if d, _, _ := xollama.ParseKeepAlive("forever"); d != func() api.Duration {
		var x api.Duration
		_ = json.Unmarshal([]byte(`-1`), &x)
		return x
	}().Duration {
		t.Error("forever is not -1")
	}
}

func TestABadKeepAliveIsRefusedAndAGoodOneRaisesNoVersion(t *testing.T) {
	bad := &xollama.Config{Version: 1, KeepAlive: "soon"}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "keep_alive") {
		t.Fatalf("keep_alive soon: %v", err)
	}
	c := &xollama.Config{Version: 1, KeepAlive: xollama.KeepAliveForever}
	if c.IsZero() {
		t.Fatal("a config stating only keep_alive reads as empty")
	}
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := xollama.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	// An older build reads keep_alive as an unknown field and runs the model
	// the same, so it must not raise the floor and lock older builds out.
	if parsed.Version != xollama.SchemaVersionBase || parsed.KeepAlive != "-1" {
		t.Fatalf("version %d keep_alive %q", parsed.Version, parsed.KeepAlive)
	}
	if s, err := xollama.NormalizeKeepAlive("Forever"); err != nil || s != "-1" {
		t.Fatalf("forever stored as %q (%v)", s, err)
	}
}
