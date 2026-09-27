package api

import (
	"context"
	"net/http"
	"sync"
)

// XollamaAPIKeyPath manages the server's local API key
// (docs/xollama/api-key.mdx). The server answers it only from this machine,
// never through a proxy, and with the current key when one is set.
const XollamaAPIKeyPath = "/api/xollama/api-key"

// APIKeyRequest asks the server to change its key. Action is "generate" (the
// server makes a 256-bit key and returns it once), "set" (Key becomes the
// key) or "remove" (the server is open again). "status" changes nothing.
type APIKeyRequest struct {
	Action string `json:"action"`
	Key    string `json:"key,omitempty"`
}

// APIKeyResponse is the server's answer: whether a key is required now and
// where it comes from ("file", or "env" when XOLLAMA_API_KEY in the server's
// environment sets it and it cannot be changed here). Key is set only on
// "generate", the one time the key is shown.
type APIKeyResponse struct {
	Required bool   `json:"required"`
	Source   string `json:"source,omitempty"`
	Key      string `json:"key,omitempty"`
}

// APIKey sends req to the server's XollamaAPIKeyPath.
func (c *Client) APIKey(ctx context.Context, req *APIKeyRequest) (*APIKeyResponse, error) {
	var resp APIKeyResponse
	if err := c.do(ctx, http.MethodPost, XollamaAPIKeyPath, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

var (
	processKeyMu sync.RWMutex
	processKey   string
)

// SetProcessAPIKey is the server's own token for its calls to itself; a
// client made by ClientFromEnvironment in the server's process sends it when
// no key is configured. Nothing else should call it.
func SetProcessAPIKey(k string) {
	processKeyMu.Lock()
	processKey = k
	processKeyMu.Unlock()
}

func processAPIKey() string {
	processKeyMu.RLock()
	defer processKeyMu.RUnlock()
	return processKey
}
