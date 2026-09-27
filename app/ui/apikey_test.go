//go:build windows || darwin

package ui

import (
	"net/http"
	"testing"
)

func TestTheDesktopProxySendsTheUsersKey(t *testing.T) {
	t.Setenv("XOLLAMA_API_KEY", "desktop-key-aaaaaaaaaa")
	for name, c := range map[string]struct {
		set         map[string]string
		wantAuth    string
		wantXAPIKey string
	}{
		"no credentials":         {nil, "Bearer desktop-key-aaaaaaaaaa", ""},
		"own Authorization kept": {map[string]string{"Authorization": "Bearer mine"}, "Bearer mine", ""},
		"own x-api-key kept":     {map[string]string{"x-api-key": "mine"}, "", "mine"},
	} {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:22434/api/tags", nil)
		for k, v := range c.set {
			req.Header.Set(k, v)
		}
		withAPIKey(req)
		if req.Header.Get("Authorization") != c.wantAuth || req.Header.Get("x-api-key") != c.wantXAPIKey {
			t.Errorf("%s: headers %v", name, req.Header)
		}
	}
}

func TestTheDesktopProxyAddsNothingWithoutAKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XOLLAMA_API_KEY", "")
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:22434/api/tags", nil)
	withAPIKey(req)
	if len(req.Header) != 0 {
		t.Fatalf("headers without a key: %v", req.Header)
	}
}
