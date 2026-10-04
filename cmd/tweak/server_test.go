package tweak

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// fakeKeyServer answers the key route as the server would, recording the
// actions it was sent.
func fakeKeyServer(t *testing.T, actions *[]api.APIKeyRequest) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XOLLAMA_API_KEY", "")
	required := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != api.XollamaAPIKeyPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req api.APIKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		*actions = append(*actions, req)
		resp := api.APIKeyResponse{}
		switch req.Action {
		case "generate":
			required, resp.Key = true, "xok_generated-aaaaaaaaaaaaaaaa"
		case "set":
			required = true
		case "remove":
			required = false
		}
		resp.Required = required
		if required {
			resp.Source = "file"
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XOLLAMA_HOST", srv.URL)
}

func runServerCmd(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := serverCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func TestGenerateSavesThisUsersCopyAndShowsTheKeyOnce(t *testing.T) {
	var actions []api.APIKeyRequest
	fakeKeyServer(t, &actions)
	out := runServerCmd(t, "", "--api-key=generate")
	if strings.Count(out, "xok_generated-aaaaaaaaaaaaaaaa") != 1 {
		t.Fatalf("the key must be shown exactly once:\n%s", out)
	}
	p, _ := envconfig.ClientKeyPath()
	data, err := os.ReadFile(p)
	if err != nil || strings.TrimSpace(string(data)) != "xok_generated-aaaaaaaaaaaaaaaa" {
		t.Fatalf("client key file = %q, %v", data, err)
	}
	if fi, _ := os.Stat(p); os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
		t.Fatalf("client key file mode = %v, want 0600", fi.Mode().Perm())
	}
	if !strings.Contains(out, "TLS") {
		t.Fatalf("no TLS reminder:\n%s", out)
	}
}

func TestSetReadsTheKeyFromStdinNotTheCommandLine(t *testing.T) {
	var actions []api.APIKeyRequest
	fakeKeyServer(t, &actions)
	out := runServerCmd(t, "my-own-key-aaaaaaaaaa\n", "--api-key=set")
	last := actions[len(actions)-1]
	if last.Action != "set" || last.Key != "my-own-key-aaaaaaaaaa" {
		t.Fatalf("sent %+v", last)
	}
	if strings.Contains(out, "my-own-key-aaaaaaaaaa") {
		t.Fatalf("a key the user typed is echoed back:\n%s", out)
	}
}

func TestRemoveDropsThisUsersCopy(t *testing.T) {
	var actions []api.APIKeyRequest
	fakeKeyServer(t, &actions)
	runServerCmd(t, "", "--api-key=generate")
	runServerCmd(t, "", "--api-key=remove")
	p, _ := envconfig.ClientKeyPath()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("client key file still there: %v", err)
	}
}

func TestTheWalkOffersRemoveOnlyWhenAKeyIsSet(t *testing.T) {
	var actions []api.APIKeyRequest
	fakeKeyServer(t, &actions)
	// A bare `tweak server` asks which part first; the key is the second.
	out := runServerCmd(t, "api-key\n\n")
	if strings.Contains(out, "remove it") || !strings.Contains(out, "API key: none") {
		t.Fatalf("open server walk:\n%s", out)
	}
	if len(actions) != 1 || actions[0].Action != "status" {
		t.Fatalf("keep must change nothing: %+v", actions)
	}
	runServerCmd(t, "", "--api-key=generate")
	if out := runServerCmd(t, "remove\n", "--api-key"); !strings.Contains(out, "API key removed") {
		t.Fatalf("keyed server walk:\n%s", out)
	}
}

func TestAnUnknownActionIsRefused(t *testing.T) {
	var actions []api.APIKeyRequest
	fakeKeyServer(t, &actions)
	cmd := serverCommand(Options{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--api-key=rotate"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("rotate accepted")
	}
}

func TestTheServerOffersOnlyTheSettingsAServerMayDefault(t *testing.T) {
	names := serverFields()
	for _, want := range []string{"engine", "kv-k", "kv-unified", "kv-residency", "slots", "slots-max", "session-pool", "spec-type"} {
		if !contains(names, want) {
			t.Errorf("%s is missing from the server's defaults", want)
		}
	}
	for _, own := range []string{"dca", "dca-chunk", "devices", "device-backend", "council"} {
		if contains(names, own) {
			t.Errorf("%s is a model's own setting, offered as a server default", own)
		}
	}
}

func TestServerDefaultsAreSentByFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var sent []api.SettingsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case api.XollamaSettingsPath:
			var req api.SettingsRequest
			json.NewDecoder(r.Body).Decode(&req)
			sent = append(sent, req)
			json.NewEncoder(w).Encode(api.SettingsResponse{Path: "/x", Defaults: req.Defaults})
		case "/api/ps":
			json.NewEncoder(w).Encode(api.ProcessResponse{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XOLLAMA_HOST", srv.URL)

	runServerCmd(t, "", "--kv-k=q8_0", "--kv-v=q8_0", "--slots=on", "-y")
	if len(sent) != 2 || sent[1].Defaults == nil {
		t.Fatalf("sent %+v", sent)
	}
	d := sent[1].Defaults
	if d.KV == nil || d.KV.K != "q8_0" || d.KV.V != "q8_0" || d.Slots == nil || d.Slots.Dynamic == nil || !*d.Slots.Dynamic {
		t.Fatalf("defaults sent: %+v", d)
	}
}

func TestTheEnginePoliciesAreModelAndServerSettings(t *testing.T) {
	for _, name := range []string{"kv-rolling-window", "mtp-policy", "fit", "vram-target"} {
		if _, ok := fieldByName(name); !ok {
			t.Errorf("tweak model has no --%s", name)
		}
		if !contains(serverFields(), name) {
			t.Errorf("tweak server has no --%s", name)
		}
	}
}
