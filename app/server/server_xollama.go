package server

import (
	"net"

	"github.com/ollama/ollama/envconfig"
)

// exposeXollama makes the app's Expose setting reach the server. xollama
// binds XOLLAMA_HOST only (envconfig.Host, the host-namespace hook), so
// upstream's OLLAMA_HOST=0.0.0.0 left an exposed install on 127.0.0.1:
// measured on eleven2go, 0.34.2-xollama.1 with Expose on was unreachable from
// the LAN. The port stays the one the server would use anyway: an operator's
// XOLLAMA_HOST port, else envconfig.DefaultPort.
func exposeXollama(env map[string]string) {
	port := envconfig.DefaultPort
	if v, ok := env["XOLLAMA_HOST"]; ok && v != "" {
		if _, p, err := net.SplitHostPort(v); err == nil && p != "" {
			port = p
		}
	}
	env["XOLLAMA_HOST"] = net.JoinHostPort("0.0.0.0", port)
}
