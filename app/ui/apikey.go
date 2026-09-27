package ui

import (
	"net/http"

	"github.com/ollama/ollama/envconfig"
)

// withAPIKey gives a request the desktop UI proxies to the server this user's
// local API key (docs/xollama/api-key.mdx), unless it already carries one.
// The proxy only ever targets the configured server, so the key goes nowhere
// else.
func withAPIKey(req *http.Request) {
	k := envconfig.ClientAPIKey()
	if k == "" || req.Header.Get("Authorization") != "" || req.Header.Get("x-api-key") != "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+k)
}
