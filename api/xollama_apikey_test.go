package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/envconfig"
)

func keyEnv(t *testing.T, key string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XOLLAMA_API_KEY", key)
	SetProcessAPIKey("")
	t.Cleanup(func() { SetProcessAPIKey("") })
}

// recorder answers /api/version and records what each request carried.
func recorder(t *testing.T, seen *[]http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":"1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheConfiguredServerGetsTheKey(t *testing.T) {
	var seen []http.Header
	srv := recorder(t, &seen)
	keyEnv(t, "k-aaaaaaaaaaaaaaaa")
	t.Setenv("XOLLAMA_HOST", srv.URL)
	c, err := ClientFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Version(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := seen[0].Get("Authorization"); got != "Bearer k-aaaaaaaaaaaaaaaa" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestAClientForAnotherHostNeverGetsTheKey(t *testing.T) {
	var seen []http.Header
	srv := recorder(t, &seen)
	keyEnv(t, "k-aaaaaaaaaaaaaaaa")
	SetProcessAPIKey("process-token")
	u, _ := url.Parse(srv.URL)
	if _, err := NewClient(u, http.DefaultClient).Version(t.Context()); err != nil {
		t.Fatal(err)
	}
	if seen[0].Get("Authorization") != "" || seen[0].Get("x-api-key") != "" {
		t.Fatalf("NewClient sent a key: %v", seen[0])
	}
}

func TestTheKeyFileIsUsedWhenTheEnvironmentHasNone(t *testing.T) {
	var seen []http.Header
	srv := recorder(t, &seen)
	keyEnv(t, "")
	t.Setenv("XOLLAMA_HOST", srv.URL)
	p, _ := envconfig.ClientKeyPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("from-file-aaaaaaaaaa\n"), 0o600)
	c, _ := ClientFromEnvironment()
	c.Version(t.Context())
	if got := seen[0].Get("Authorization"); got != "Bearer from-file-aaaaaaaaaa" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestInsideTheServerItsOwnTokenWinsOverAKeyFile(t *testing.T) {
	keyEnv(t, "")
	p, _ := envconfig.ClientKeyPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("stale-key-aaaaaaaaaa\n"), 0o600)
	SetProcessAPIKey("process-token")
	c, _ := ClientFromEnvironment()
	if c.apiKey != "process-token" {
		t.Fatalf("apiKey = %q, want the process token", c.apiKey)
	}
}

func TestASignedRequestCarriesTheKeyInXAPIKey(t *testing.T) {
	c := &Client{apiKey: "k"}
	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	r.Header.Set("Authorization", "ollama-signature")
	c.setAPIKey(r)
	if r.Header.Get("Authorization") != "ollama-signature" || r.Header.Get("x-api-key") != "k" {
		t.Fatalf("headers = %v", r.Header)
	}
}

func TestARedirectToAnotherHostIsRefused(t *testing.T) {
	var seen []http.Header
	elsewhere := recorder(t, &seen)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			http.Redirect(w, r, elsewhere.URL+"/api/version", http.StatusTemporaryRedirect)
			return
		}
	}))
	defer origin.Close()
	keyEnv(t, "k-aaaaaaaaaaaaaaaa")
	t.Setenv("XOLLAMA_HOST", origin.URL)
	c, _ := ClientFromEnvironment()
	_, err := c.Version(t.Context())
	if !errors.Is(err, errRedirectWithKey) {
		t.Fatalf("err = %v, want errRedirectWithKey", err)
	}
	if len(seen) != 0 {
		t.Fatalf("the other host was reached: %v", seen)
	}
}

func TestARedirectOnTheSameHostIsFollowed(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			http.Redirect(w, r, "/v2/version", http.StatusTemporaryRedirect)
			return
		}
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"version":"1"}`))
	}))
	defer srv.Close()
	keyEnv(t, "k-aaaaaaaaaaaaaaaa")
	t.Setenv("XOLLAMA_HOST", srv.URL)
	c, _ := ClientFromEnvironment()
	if _, err := c.Version(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer k-aaaaaaaaaaaaaaaa" {
		t.Fatalf("Authorization after redirect = %q", got)
	}
}

func challenge(realm bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if realm {
			w.Header().Set("WWW-Authenticate", `Bearer realm="xollama"`)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			w.WriteHeader(http.StatusTeapot) // the probe must never send a key
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"wrong API key"}`))
	}))
}

func TestALocalKeyRefusalIsNotAnOllamaComSignIn(t *testing.T) {
	keyEnv(t, "")
	for _, realm := range []bool{true, false} {
		srv := challenge(realm)
		u, _ := url.Parse(srv.URL)
		_, err := NewClient(u, http.DefaultClient).Version(t.Context())
		srv.Close()
		var auth AuthorizationError
		var st StatusError
		switch {
		case realm && (!errors.As(err, &st) || !strings.Contains(st.ErrorMessage, "XOLLAMA_API_KEY")):
			t.Fatalf("keyed xollama: err = %#v", err)
		case !realm && !errors.As(err, &auth):
			t.Fatalf("other 401: err = %#v, want AuthorizationError", err)
		}
	}
}

func TestTheProbeKnowsAKeyedXollamaWithoutSendingTheKey(t *testing.T) {
	keyEnv(t, "k-aaaaaaaaaaaaaaaa")
	for _, realm := range []bool{true, false} {
		srv := challenge(realm)
		u, _ := url.Parse(srv.URL)
		up, x := probeHost(context.Background(), u)
		srv.Close()
		if !up || x != realm {
			t.Fatalf("realm %v: up %v xollama %v", realm, up, x)
		}
	}
}

// Off means off on the client too: with no key anywhere, nothing is added.
func TestAClientWithoutAKeySendsNone(t *testing.T) {
	var seen []http.Header
	srv := recorder(t, &seen)
	keyEnv(t, "")
	t.Setenv("XOLLAMA_HOST", srv.URL)
	c, _ := ClientFromEnvironment()
	if c.HasAPIKey() {
		t.Fatal("HasAPIKey with no key configured")
	}
	if _, err := c.Version(t.Context()); err != nil {
		t.Fatal(err)
	}
	if seen[0].Get("Authorization") != "" || seen[0].Get("x-api-key") != "" {
		t.Fatalf("headers sent without a key: %v", seen[0])
	}
}
