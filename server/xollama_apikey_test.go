package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// keyHome gives the test its own home, no key in the environment and a fresh
// gate, and returns the server key file's path.
func keyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XOLLAMA_API_KEY", "")
	keyGate = &apiKeyGate{failures: map[string]*keyFailures{}}
	t.Cleanup(func() { keyGate = &apiKeyGate{failures: map[string]*keyFailures{}} })
	return filepath.Join(home, ".ollama", envconfig.ServerKeyFile)
}

func setServerKey(t *testing.T, key string) {
	t.Helper()
	p := keyHome(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, envconfig.ServerKeyFileContent(key), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keyRoutes(t *testing.T) http.Handler {
	t.Helper()
	h, err := (&Server{}).GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serve(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if ra := hdr["remote"]; ra != "" {
		req.RemoteAddr = ra
		req.Header.Del("remote")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const testKey = "xok_test-key-aaaaaaaaaaaaaaaa"

func TestAServerWithoutAKeyIsOpen(t *testing.T) {
	keyHome(t)
	if w := serve(keyRoutes(t), http.MethodGet, "/api/version", nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no key configured", w.Code)
	}
}

func TestAKeyedServerRefusesEveryRouteWithoutTheKey(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	for _, r := range [][2]string{
		{http.MethodGet, "/"},
		{http.MethodHead, "/"},
		{http.MethodGet, "/api/version"},
		{http.MethodGet, "/api/tags"},
		{http.MethodPost, "/api/chat"},
		{http.MethodPost, "/api/generate"},
		{http.MethodPost, "/api/show"},
		{http.MethodDelete, "/api/delete"},
		{http.MethodPost, "/api/pull"},
		{http.MethodGet, "/api/xollama"},
		{http.MethodGet, "/api/engine"},
		{http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, api.XollamaAPIKeyPath},
		{http.MethodGet, "/no/such/route"},
	} {
		w := serve(h, r[0], r[1], nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", r[0], r[1], w.Code)
		}
		if got := w.Header().Get("WWW-Authenticate"); got != `Bearer realm="xollama"` {
			t.Errorf("%s %s: WWW-Authenticate = %q", r[0], r[1], got)
		}
	}
}

func TestTheKeyIsAcceptedAsBearerOrXAPIKey(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	for name, hdr := range map[string]map[string]string{
		"bearer":           {"Authorization": "Bearer " + testKey},
		"bearer lowercase": {"Authorization": "bearer " + testKey},
		"x-api-key":        {"x-api-key": testKey},
	} {
		if w := serve(h, http.MethodGet, "/api/version", hdr); w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, w.Code)
		}
	}
}

func TestAWrongOrMisplacedKeyIsRefused(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
	}{
		"wrong bearer":   {"/api/version", map[string]string{"Authorization": "Bearer " + testKey + "x"}},
		"wrong x-key":    {"/api/version", map[string]string{"x-api-key": "nope-nope-nope-nope"}},
		"basic scheme":   {"/api/version", map[string]string{"Authorization": "Basic " + testKey}},
		"query string":   {"/api/version?api_key=" + testKey, nil},
		"digest as key":  {"/api/version", map[string]string{"Authorization": "Bearer " + string(envconfig.ServerKeyFileContent(testKey))}},
		"empty bearer":   {"/api/version", map[string]string{"Authorization": "Bearer "}},
		"prefix of key":  {"/api/version", map[string]string{"x-api-key": testKey[:10]}},
		"key with space": {"/api/version", map[string]string{"x-api-key": testKey + " x"}},
	} {
		if w := serve(h, http.MethodGet, c.path, c.hdr); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, w.Code)
		}
		keyGate.failures = map[string]*keyFailures{}
	}
}

func TestRepeatedWrongKeysAreThrottledPerPeerNotPerForwardedHeader(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	for i := range maxKeyFailures {
		w := serve(h, http.MethodGet, "/api/version", map[string]string{"x-api-key": "wrong-wrong-wrong", "X-Forwarded-For": "10.0.0." + string(rune('1'+i%9))})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, w.Code)
		}
	}
	// A spoofed X-Forwarded-For must not buy a fresh budget, and while
	// throttled even the right key waits: the throttle answers first.
	w := serve(h, http.MethodGet, "/api/version", map[string]string{"x-api-key": testKey, "X-Forwarded-For": "10.9.9.9"})
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d Retry-After %q, want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	// Another peer is unaffected.
	if w := serve(h, http.MethodGet, "/api/version", map[string]string{"x-api-key": testKey, "remote": "198.51.100.7:4000"}); w.Code != http.StatusOK {
		t.Fatalf("other peer: status = %d, want 200", w.Code)
	}
}

func TestTheKeyIsStrippedBeforeAnyHandler(t *testing.T) {
	setServerKey(t, testKey)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use((&Server{}).apiKeyMiddleware())
	var seen http.Header
	r.GET("/x", func(c *gin.Context) { seen = c.Request.Header.Clone() })
	for _, hdr := range []map[string]string{{"Authorization": "Bearer " + testKey}, {"x-api-key": testKey}} {
		if w := serve(r, http.MethodGet, "/x", hdr); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if seen.Get("Authorization") != "" || seen.Get("x-api-key") != "" {
			t.Fatalf("handler saw the key: %v", seen)
		}
	}
}

func TestExemptRequestsAreNotChallenged(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	w := serve(h, http.MethodOptions, "/api/chat", map[string]string{"Origin": "http://localhost", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "x-api-key"})
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("preflight got 401")
	}
	if !strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), "x-api-key") {
		t.Fatalf("preflight does not allow x-api-key: %q", w.Header().Get("Access-Control-Allow-Headers"))
	}
	if w := serve(h, http.MethodGet, "/api/codex/v1/models", nil); w.Code == http.StatusUnauthorized {
		t.Fatalf("codex proxy got 401")
	}
}

func TestTheServersOwnTokenPasses(t *testing.T) {
	setServerKey(t, testKey)
	h := keyRoutes(t)
	if w := serve(h, http.MethodGet, "/api/version", map[string]string{"Authorization": "Bearer " + processToken}); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestAKeyFromTheEnvironmentWinsAndCannotBeChangedHere(t *testing.T) {
	keyHome(t)
	t.Setenv("XOLLAMA_API_KEY", testKey)
	h := keyRoutes(t)
	if w := serve(h, http.MethodGet, "/api/version", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	w := adminCall(h, "127.0.0.1:5000", "status", "", map[string]string{"x-api-key": testKey})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"env"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	req := httptest.NewRequest(http.MethodPost, api.XollamaAPIKeyPath, strings.NewReader(`{"action":"remove"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("x-api-key", testKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("remove under env key: status = %d, want 409", rec.Code)
	}
}

func adminCall(h http.Handler, remote, action, key string, hdr map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(api.APIKeyRequest{Action: action, Key: key})
	req := httptest.NewRequest(http.MethodPost, api.XollamaAPIKeyPath, strings.NewReader(string(b)))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestTheKeyIsManagedOnlyFromThisMachineAndNotThroughAProxy(t *testing.T) {
	p := keyHome(t)
	h := keyRoutes(t)
	// An open server: someone on the network must not be able to set a key
	// and lock the owner out.
	if w := adminCall(h, "192.0.2.10:4000", "generate", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("remote generate: status = %d, want 403", w.Code)
	}
	for _, hd := range []string{"X-Forwarded-For", "Forwarded", "Via", "X-Real-Ip", "X-Forwarded-Host"} {
		if w := adminCall(h, "127.0.0.1:4000", "generate", "", map[string]string{hd: "192.0.2.10"}); w.Code != http.StatusForbidden {
			t.Fatalf("proxied (%s) generate: status = %d, want 403", hd, w.Code)
		}
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("a refused call left a key file: %v", err)
	}
}

func TestGenerateSetAndRemoveTheKey(t *testing.T) {
	p := keyHome(t)
	h := keyRoutes(t)

	w := adminCall(h, "127.0.0.1:4000", "generate", "", nil)
	var resp api.APIKeyResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}
	if !resp.Required || resp.Source != "file" || !strings.HasPrefix(resp.Key, "xok_") || len(resp.Key) < 40 {
		t.Fatalf("generate: %+v", resp)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), resp.Key) {
		t.Fatalf("the key file holds the key itself, not its digest")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	if w := serve(h, http.MethodGet, "/api/version", map[string]string{"x-api-key": resp.Key}); w.Code != http.StatusOK {
		t.Fatalf("generated key refused: %d", w.Code)
	}

	// Changing a key needs the current one, even from loopback.
	if w := adminCall(h, "127.0.0.1:4000", "remove", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("remove without the key: status = %d, want 401", w.Code)
	}
	auth := map[string]string{"Authorization": "Bearer " + resp.Key}
	if w := adminCall(h, "127.0.0.1:4000", "set", "short", auth); w.Code != http.StatusBadRequest {
		t.Fatalf("short key: status = %d, want 400", w.Code)
	}
	if w := adminCall(h, "127.0.0.1:4000", "set", testKey, auth); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if w := serve(h, http.MethodGet, "/api/version", map[string]string{"x-api-key": resp.Key}); w.Code != http.StatusUnauthorized {
		t.Fatalf("replaced key still accepted: %d", w.Code)
	}
	if w := adminCall(h, "127.0.0.1:4000", "remove", "", map[string]string{"x-api-key": testKey}); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if w := serve(h, http.MethodGet, "/api/version", nil); w.Code != http.StatusOK {
		t.Fatalf("after remove: status = %d, want 200", w.Code)
	}
	if w := adminCall(h, "127.0.0.1:4000", "rotate", "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: status = %d, want 400", w.Code)
	}
}
