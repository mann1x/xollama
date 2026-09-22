package tweak

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// The command talks to a server, not to the model store, so that it works
// against a remote one. These tests hold that wire: /api/show must CARRY the
// config back (which is why api.ShowResponse gained the field) and /api/create
// must receive it with From set to the model itself, so nothing but the
// xollama.json layer changes.
func TestItReadsShowAndWritesThroughCreate(t *testing.T) {
	var got api.CreateRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			json.NewEncoder(w).Encode(api.ShowResponse{
				Xollama: &xollama.Config{
					Version: 1,
					Engine:  xollama.EngineOpencoti,
					DCA:     &xollama.DCA{Enabled: boolp(false)},
				},
			})
		case "/api/create":
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("create body: %v", err)
			}
			json.NewEncoder(w).Encode(api.ProgressResponse{Status: "success"})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("XOLLAMA_HOST", srv.URL)

	cmd := modelCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"m:test", "--dca=on", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	if got.Model != "m:test" || got.From != "m:test" {
		t.Fatalf("create request = model %q from %q, want both m:test", got.Model, got.From)
	}
	if got.Xollama == nil {
		t.Fatal("create request carried no xollama config")
	}
	// Read from show, changed by the flag, everything else preserved.
	if got.Xollama.Engine != xollama.EngineOpencoti {
		t.Fatalf("engine = %q, want the one show reported", got.Xollama.Engine)
	}
	if got.Xollama.DCA == nil || !*got.Xollama.DCA.Enabled {
		t.Fatalf("dca = %+v, want the flag's value", got.Xollama.DCA)
	}
	// Nothing else about the model is named, so nothing else can change.
	if got.Template != "" || got.System != "" || len(got.Parameters) != 0 || len(got.Files) != 0 {
		t.Fatalf("create request touched something other than the config: %+v", got)
	}
	if !strings.Contains(out.String(), `"version": 1`) {
		t.Fatalf("--json must print what is stored\n%s", out.String())
	}
}

// A model with no config is not an error: it is the ordinary starting point,
// and the command must offer the same questions it would for one that has one.
func TestAModelWithNoConfigStartsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			json.NewEncoder(w).Encode(api.ShowResponse{})
			return
		}
		json.NewEncoder(w).Encode(api.ProgressResponse{Status: "success"})
	}))
	defer srv.Close()
	t.Setenv("XOLLAMA_HOST", srv.URL)

	client, err := api.ClientFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := showConfig(t.Context(), client, "m:test")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsZero() {
		t.Fatalf("want an empty config, got %+v", cfg)
	}
}

// --clear sends a config that is present and empty. nil would mean "say
// nothing", which inherits the layer this run was asked to remove.
func TestClearSendsAnEmptyConfigRatherThanNothing(t *testing.T) {
	var raw map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			json.NewEncoder(w).Encode(api.ShowResponse{
				Xollama: &xollama.Config{Version: 1, Engine: xollama.EngineOpencoti},
			})
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &raw)
		json.NewEncoder(w).Encode(api.ProgressResponse{Status: "success"})
	}))
	defer srv.Close()
	t.Setenv("XOLLAMA_HOST", srv.URL)

	cmd := modelCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"m:test", "--clear"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	if _, ok := raw["xollama"]; !ok {
		t.Fatalf("--clear sent no xollama key at all, which inherits the layer: %v", raw)
	}
}
