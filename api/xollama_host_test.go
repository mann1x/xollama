package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// xollamaServer answers the identity route the way this fork does.
func xollamaServer(t *testing.T) *url.URL {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != XollamaIdentityPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"xollama":true,"version":"1.2.3"}`))
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// stockOllamaServer answers /api/version and 404s everything else, which is
// what an ollama that has never heard of this fork does.
func stockOllamaServer(t *testing.T) *url.URL {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version":"0.12.3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// deadHost is an address nothing is listening on: a server taken down keeps its
// port, and binding it again would race.
func deadHost(t *testing.T) *url.URL {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	return u
}

func TestTheCLIFallsBackToTheStockPortOnlyForAnXollama(t *testing.T) {
	tests := []struct {
		name       string
		configured func(*testing.T) *url.URL
		fallback   func(*testing.T) *url.URL
		explicit   bool
		wantErr    bool
		wantWhich  string // "configured" or "fallback"
	}{
		{
			name:       "an xollama on the default port is used and the fallback is never consulted",
			configured: xollamaServer,
			fallback:   stockOllamaServer,
			wantWhich:  "configured",
		},
		{
			name:       "nothing on the default port, an xollama on the stock one, so use it",
			configured: deadHost,
			fallback:   xollamaServer,
			wantWhich:  "fallback",
		},
		{
			name:       "nothing on the default port and a STOCK ollama on the stock one is refused",
			configured: deadHost,
			fallback:   stockOllamaServer,
			wantErr:    true,
		},
		{
			name:       "nothing anywhere returns our own host, so the error names our port",
			configured: deadHost,
			fallback:   deadHost,
			wantWhich:  "configured",
		},
		{
			// The upgrade case. An xollama predating the identity route 404s it
			// exactly as a stock ollama does, so the default port must not apply
			// the identity test -- being up is enough there.
			name:       "an older xollama on the default port is still used",
			configured: stockOllamaServer,
			fallback:   deadHost,
			wantWhich:  "configured",
		},
		{
			// An explicit host is the operator's instruction, not a hint.
			name:       "an explicit host is used even when a stock ollama is reachable",
			configured: deadHost,
			fallback:   stockOllamaServer,
			explicit:   true,
			wantWhich:  "configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured, fallback := tt.configured(t), tt.fallback(t)
			got, err := resolveHostFrom(context.Background(), configured, fallback, tt.explicit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a refusal, got %s", got)
				}
				if !errors.Is(err, ErrStockOllamaOnFallbackPort) {
					t.Fatalf("error does not wrap ErrStockOllamaOnFallbackPort: %v", err)
				}
				// The message has to tell the operator how to proceed, or the
				// refusal is just an obstacle.
				if !strings.Contains(err.Error(), "XOLLAMA_HOST") {
					t.Errorf("refusal does not say how to override it: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := configured
			if tt.wantWhich == "fallback" {
				want = fallback
			}
			if got.String() != want.String() {
				t.Errorf("resolved %s, want the %s host %s", got, tt.wantWhich, want)
			}
		})
	}
}

// The regression this nearly shipped with: an xollama old enough to 404 the
// identity route must still be recognised, by the fork's name in its version.
func TestAnXollamaTooOldForTheIdentityRouteIsStillRecognised(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			// Verbatim from the server running on this host, which 404s
			// /api/xollama and is nonetheless xollama.
			_, _ = w.Write([]byte(`{"version":"0.34.2-xollama-12644ae4"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}

	up, isXollama := probeHost(context.Background(), u)
	if !up || !isXollama {
		t.Fatalf("an older xollama read as up=%v isXollama=%v, want both true", up, isXollama)
	}

	// And therefore it is used as the fallback rather than refused.
	got, err := resolveHostFrom(context.Background(), deadHost(t), u, false)
	if err != nil {
		t.Fatalf("refused a real xollama on the stock port: %v", err)
	}
	if got.String() != u.String() {
		t.Errorf("resolved %s, want %s", got, u)
	}
}

// A stock ollama's version must not be read as the fork's.
func TestAStockOllamaVersionDoesNotNameTheFork(t *testing.T) {
	if versionNamesTheFork(context.Background(), stockOllamaServer(t)) {
		t.Error("0.12.3 was read as an xollama version")
	}
}

// The probe must not be fooled by a server that answers everything with 200.
func TestAServerThatAnswersEverythingIsNotMistakenForXollama(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}

	up, isXollama := probeHost(context.Background(), u)
	if !up {
		t.Error("a server answering 200 should read as up")
	}
	if isXollama {
		t.Error("an empty JSON body must not identify as xollama")
	}
}
