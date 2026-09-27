package server

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ollama/ollama/app/store"
	"github.com/ollama/ollama/envconfig"
)

// Expose binds every interface on the port xollama listens on; without it the
// app sets no address at all.
func TestExposeBindsXollamasAddress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expose bool
		host   string
		want   string
	}{
		{"expose", true, "", "XOLLAMA_HOST=0.0.0.0:" + envconfig.DefaultPort},
		{"expose keeps the operator's port", true, "127.0.0.1:23000", "XOLLAMA_HOST=0.0.0.0:23000"},
		{"no expose", false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XOLLAMA_HOST", tc.host)
			st := &store.Store{DBPath: filepath.Join(t.TempDir(), "db.sqlite")}
			defer st.Close()
			st.SetSettings(store.Settings{Expose: tc.expose})
			cmd, err := (&Server{store: st}).cmd(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(cmd.Env, func(e string) bool { return len(e) > 13 && e[:13] == "XOLLAMA_HOST=" && e != "XOLLAMA_HOST=" })
			got := ""
			if i >= 0 {
				got = cmd.Env[i]
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
