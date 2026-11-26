package tweak

import (
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

// --keep-alive stores forever as -1, keeps a duration as written, refuses a
// value no request could carry, and is a model's setting only: it is not one
// of the server's defaults.
func TestTheKeepAliveFieldStoresWhatARequestWouldSend(t *testing.T) {
	f, ok := fieldByName("keep-alive")
	if !ok {
		t.Fatal("no keep-alive field")
	}
	c := &xollama.Config{Version: 1}
	for in, want := range map[string]string{"forever": "-1", "30m": "30m", "120": "120", "": "", "unset": "", "Forever": "-1"} {
		if err := f.set(c, in); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := f.get(c); got != want {
			t.Fatalf("%q stored as %q, want %q", in, got, want)
		}
	}
	if err := f.set(c, "soon"); err == nil {
		t.Fatal("keep-alive soon was accepted")
	}
	for _, name := range serverFields() {
		if name == "keep-alive" {
			t.Fatal("keep-alive is offered as a server default; it is the model's own")
		}
	}
}
