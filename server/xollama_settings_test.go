package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/xollama"
)

func settingsCall(t *testing.T, h http.Handler, remote string, req api.SettingsRequest, hdr map[string]string) (*httptest.ResponseRecorder, api.SettingsResponse) {
	t.Helper()
	b, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, api.XollamaSettingsPath, strings.NewReader(string(b)))
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp api.SettingsResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return w, resp
}

func settingsHome(t *testing.T) string {
	t.Helper()
	keyHome(t)
	envconfig.ReloadSettings()
	t.Cleanup(envconfig.ReloadSettings)
	p, err := envconfig.SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func str(s string) *string { return &s }

func findEnv(envs []api.SettingsEnv, name string) (api.SettingsEnv, bool) {
	i := slices.IndexFunc(envs, func(e api.SettingsEnv) bool { return e.Name == name })
	if i < 0 {
		return api.SettingsEnv{}, false
	}
	return envs[i], true
}

func TestSettingsAreManagedOnlyFromThisMachineAndNotThroughAProxy(t *testing.T) {
	p := settingsHome(t)
	h := keyRoutes(t)
	set := api.SettingsRequest{Envs: map[string]*string{"OLLAMA_KV_CACHE_TYPE": str("q8_0")}}
	if w, _ := settingsCall(t, h, "192.0.2.10:4000", set, nil); w.Code != http.StatusForbidden {
		t.Fatalf("remote: status = %d, want 403", w.Code)
	}
	if w, _ := settingsCall(t, h, "127.0.0.1:4000", set, map[string]string{"X-Forwarded-For": "192.0.2.10"}); w.Code != http.StatusForbidden {
		t.Fatalf("proxied: status = %d, want 403", w.Code)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("a refused call left a settings file: %v", err)
	}
}

func TestASettingIsAppliedAtOnceAndRemovedAgain(t *testing.T) {
	p := settingsHome(t)
	h := keyRoutes(t)
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "q4_0")
	t.Setenv("XOLLAMA_KV_CACHE_TYPE", "")

	w, resp := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Envs: map[string]*string{
		"OLLAMA_KV_CACHE_TYPE": str("q8_0"),
		"XOLLAMA_HOST":         str("127.0.0.1:22500"),
	}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("set: status = %d: %s", w.Code, w.Body)
	}
	if got := envconfig.KvCacheType(); got != "q8_0" {
		t.Fatalf("KvCacheType = %q after the write, want q8_0 without a restart", got)
	}
	if !slices.Equal(resp.Restart, []string{"XOLLAMA_HOST"}) {
		t.Fatalf("restart = %v, want only the listen address", resp.Restart)
	}
	e, ok := findEnv(resp.Envs, "OLLAMA_KV_CACHE_TYPE")
	if !ok || e.Source != "tweak" || e.Env == nil || *e.Env != "q4_0" {
		t.Fatalf("the listing hides what the override replaced: %+v", e)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file: %v %v", fi, err)
	}

	w, _ = settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Envs: map[string]*string{
		"OLLAMA_KV_CACHE_TYPE": nil, "XOLLAMA_HOST": nil,
	}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("unset: status = %d", w.Code)
	}
	if got := envconfig.KvCacheType(); got != "q4_0" {
		t.Fatalf("KvCacheType = %q after the removal, want the environment's q4_0", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("clearing every override left the file behind: %v", err)
	}
}

func TestTheSettingsRouteRefusesTheAPIKeyAndUnknownNames(t *testing.T) {
	p := settingsHome(t)
	h := keyRoutes(t)
	for _, name := range []string{"XOLLAMA_API_KEY", "XOLLAMA_NOT_A_THING", "PATH"} {
		w, _ := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Envs: map[string]*string{name: str("x")}}, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, w.Code)
		}
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("a refused name was written")
	}
}

func TestAKeyedServerAsksForTheKeyOnTheSettingsRoute(t *testing.T) {
	setServerKey(t, testKey)
	envconfig.ReloadSettings()
	t.Cleanup(envconfig.ReloadSettings)
	h := keyRoutes(t)
	if w, _ := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{}, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: status = %d, want 401", w.Code)
	}
	if w, _ := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{}, map[string]string{"Authorization": "Bearer " + testKey}); w.Code != http.StatusOK {
		t.Fatalf("with the key: status = %d, want 200", w.Code)
	}
}

func TestTheServersDefaultsReachTheLaunchUnderTheModelsOwn(t *testing.T) {
	settingsHome(t)
	h := keyRoutes(t)
	on := true
	def := &xollama.Config{FlashAttention: "on", Slots: &xollama.Slots{Dynamic: &on, Max: 8}}
	w, resp := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Defaults: def}, nil)
	if w.Code != http.StatusOK || resp.Defaults == nil || resp.Defaults.FlashAttention != "on" {
		t.Fatalf("set defaults: %d %s", w.Code, w.Body)
	}

	plain := &Model{ShortName: "plain"}
	if got := llamaServerConfigForModel(plain).Xollama; got == nil || got.FlashAttention != "on" || got.Slots.Max != 8 {
		t.Fatalf("a model stating nothing does not get the defaults: %+v", got)
	}
	own := &Model{ShortName: "own", Xollama: &xollama.Config{Slots: &xollama.Slots{Max: 2}}}
	got := llamaServerConfigForModel(own).Xollama
	if got.Slots.Max != 2 || got.Slots.Dynamic == nil {
		t.Fatalf("the model's own slots.max must win, the rest default: %+v", got.Slots)
	}

	if w, _ := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Defaults: &xollama.Config{}}, nil); w.Code != http.StatusOK {
		t.Fatalf("clear defaults: %d", w.Code)
	}
	if got := llamaServerConfigForModel(plain).Xollama; got != nil {
		t.Fatalf("cleared defaults still apply: %+v", got)
	}
}

func TestAModelsOwnSettingIsRefusedAsAServerDefault(t *testing.T) {
	p := settingsHome(t)
	h := keyRoutes(t)
	on := true
	w, _ := settingsCall(t, h, "127.0.0.1:4000", api.SettingsRequest{Defaults: &xollama.Config{DCA: &xollama.DCA{Enabled: &on}}}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("a refused default was written")
	}
}
