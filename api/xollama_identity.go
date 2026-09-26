package api

import (
	"context"
	"net/url"
)

// The fork's identity endpoint.
//
// xollama listens on its own port (22434) precisely so it can coexist with a
// stock ollama on 11434. That only helps if a client can tell the two apart,
// and nothing upstream distinguishes them: /api/version answers with a bare
// version string on both, and a dev build's is "0.0.0", so it identifies
// neither the fork nor the release.
//
// So the fork states it. A stock ollama answers this route with 404, which is
// the discriminator -- see ResolveHost.
const XollamaIdentityPath = "/api/xollama"

// XollamaIdentity is what XollamaIdentityPath answers.
//
// Xollama is always true when the field is present at all; it exists so a proxy
// that answers every path with 200 and an empty body is not mistaken for the
// fork.
type XollamaIdentity struct {
	Xollama bool   `json:"xollama"`
	Version string `json:"version,omitempty"`
}

// IsXollama reports whether the server at base is this fork, by the identity
// route and, for builds older than it, by the fork's name in /api/version --
// the same answer ResolveHost acts on. Nothing answering is not xollama.
func IsXollama(ctx context.Context, base *url.URL) bool {
	_, x := probeHost(ctx, base)
	return x
}
