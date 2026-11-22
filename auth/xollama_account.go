package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
)

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

// An account can also be an ollama.com API key (plans/cloud-accounts.md
// §3.8): ollama.com takes `Authorization: Bearer <key>` wherever it takes a
// signature, so Sign answers with that header for a context naming the key's
// file, and every caller that sets Sign's answer as the Authorization header
// (the cloud passthrough, api.Client) sends the key instead.

type apiKeyFileKey struct{}

// WithAPIKeyFile makes requests made with ctx authenticate with the
// ollama.com API key stored at path.
func WithAPIKeyFile(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, apiKeyFileKey{}, path)
}

// accountBearer is Sign's answer for a context naming an API key: the
// Authorization header, or the error reading the key. ok is false for any
// other context, which Sign signs as before.
func accountBearer(ctx context.Context) (token string, ok bool, err error) {
	if ctx == nil {
		return "", false, nil
	}
	p, _ := ctx.Value(apiKeyFileKey{}).(string)
	if p == "" {
		return "", false, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", true, err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", true, fmt.Errorf("the API key file %s is empty", p)
	}
	return "Bearer " + key, true, nil
}
