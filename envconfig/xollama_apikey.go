package envconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The local API key guards incoming connections to xollama's own endpoints.
// It has nothing to do with ollama.com: registry and cloud requests keep
// their own signing, and OLLAMA_API_KEY (an ollama.com key where launchers
// use it) is never read for it.
//
// The server holds only the key's SHA-256, in ServerKeyFile, or takes the key
// itself from XOLLAMA_API_KEY (containers). A client sends the key from
// XOLLAMA_API_KEY, else from ClientKeyFile, which `xollama tweak server`
// writes for the user who set it.

const (
	// ServerKeyFile holds {"api_key_sha256": "<hex>"}, mode 0600, in the
	// server's ~/.ollama.
	ServerKeyFile = "xollama-server.json"
	// ClientKeyFile holds the key itself, mode 0600, in a client's ~/.ollama.
	ClientKeyFile = "xollama-api-key"
)

type serverKeyFile struct {
	APIKeySHA256 string `json:"api_key_sha256,omitempty"`
}

// APIKeyEnv is XOLLAMA_API_KEY. Only the XOLLAMA_ spelling: OLLAMA_API_KEY is
// an ollama.com key where it is set at all.
func APIKeyEnv() string { return XollamaOnly("OLLAMA_API_KEY") }

// ServerKeyPath is where the server keeps its key's digest.
func ServerKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ollama", ServerKeyFile), nil
}

// ClientKeyPath is where a client keeps the key it sends.
func ClientKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ollama", ClientKeyFile), nil
}

// KeyDigest is the SHA-256 of a key, the form the server keeps and compares.
func KeyDigest(key string) [32]byte { return sha256.Sum256([]byte(key)) }

// ServerAPIKey reports the key the server requires: its digest, and where it
// comes from -- "env", "file", or "" when the server has none and is open.
// The environment wins, so a container's key cannot be changed from inside.
func ServerAPIKey() (digest [32]byte, source string) {
	if k := APIKeyEnv(); k != "" {
		return KeyDigest(k), "env"
	}
	p, err := ServerKeyPath()
	if err != nil {
		return digest, ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return digest, ""
	}
	var f serverKeyFile
	if json.Unmarshal(data, &f) != nil {
		return digest, ""
	}
	b, err := hex.DecodeString(strings.TrimSpace(f.APIKeySHA256))
	if err != nil || len(b) != len(digest) {
		return digest, ""
	}
	copy(digest[:], b)
	return digest, "file"
}

// ServerKeyFileContent is what ServerKeyFile holds for key.
func ServerKeyFileContent(key string) []byte {
	d := KeyDigest(key)
	b, _ := json.MarshalIndent(serverKeyFile{APIKeySHA256: hex.EncodeToString(d[:])}, "", "  ")
	return append(b, '\n')
}

// ClientAPIKey is the key a client sends to its configured server:
// XOLLAMA_API_KEY, else ClientKeyFile. Empty when there is none.
func ClientAPIKey() string {
	if k := APIKeyEnv(); k != "" {
		return k
	}
	p, err := ClientKeyPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// maskedKey is how a key appears anywhere it could be logged.
func maskedKey(k string) string {
	if k == "" {
		return ""
	}
	return "(set)"
}
