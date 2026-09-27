package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
)

// minKeyLength refuses a hand-chosen key too short to stand a scan.
const minKeyLength = 16

// newAPIKey is 256 random bits, prefixed so a leaked key is recognisable in
// a secret scan.
func newAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "xok_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// APIKeyHandler changes the server's key. The key middleware has already
// checked the current key, when there is one; on top of that the request
// must come straight from this machine -- never through a proxy, since behind
// a TLS proxy on this host every client looks like loopback -- so nobody on
// the network can set a key on an open server and lock its owner out.
func (s *Server) APIKeyHandler(c *gin.Context) {
	if !loopbackPeer(c) || proxied(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "the API key is managed only from the server's own machine, not through a proxy"})
		return
	}
	var req api.APIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	switch req.Action {
	case "status", "generate", "set", "remove":
	default:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": `action must be "status", "generate", "set" or "remove"`})
		return
	}
	_, source := envconfig.ServerAPIKey()
	if req.Action != "status" && source == "env" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "XOLLAMA_API_KEY in the server's environment sets the key; change it there"})
		return
	}
	var resp api.APIKeyResponse
	switch req.Action {
	case "status":
	case "generate", "set":
		key := strings.TrimSpace(req.Key)
		if req.Action == "generate" {
			var err error
			if key, err = newAPIKey(); err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			resp.Key = key
		} else if len(key) < minKeyLength {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "an API key must be at least 16 characters; `generate` makes a strong one"})
			return
		}
		if err := writeServerKey(envconfig.ServerKeyFileContent(key)); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	case "remove":
		p, err := envconfig.ServerKeyPath()
		if err == nil {
			err = os.Remove(p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	keyGate.reload()
	_, resp.Source = envconfig.ServerAPIKey()
	resp.Required = resp.Source != ""
	c.JSON(http.StatusOK, resp)
}

// writeServerKey replaces the key file atomically, mode 0600, owned as the
// store's files are (store-ownership).
func writeServerKey(data []byte) error {
	p, err := envconfig.ServerKeyPath()
	if err != nil {
		return err
	}
	if err := fsowner.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := fsowner.CreateTemp(filepath.Dir(p), ".xollama-server-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
