package auth

import "context"

// Signing for one of the server's Ollama Cloud accounts
// (plans/cloud-accounts.md).
//
// Upstream signs every ollama.com request with ~/.ollama/id_ed25519. With
// several accounts the server picks one per request and puts its key file on
// the request's context; Sign then signs with that file. A context without
// one signs as upstream does: off means off.

type keyFileKey struct{}

// WithKeyFile makes requests made with ctx sign with the private key at path.
func WithKeyFile(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, keyFileKey{}, path)
}

// KeyFileFrom is the key file ctx names, if any.
func KeyFileFrom(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	p, ok := ctx.Value(keyFileKey{}).(string)
	return p, ok && p != ""
}

// accountKeyPath is the key Sign uses: the context's, else the default.
func accountKeyPath(ctx context.Context, def string) string {
	if p, ok := KeyFileFrom(ctx); ok {
		return p
	}
	return def
}
