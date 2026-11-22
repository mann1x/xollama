package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func writeKey(t *testing.T, path string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// signedBy checks a Sign result ("<pubkey>:<signature>") against want.
func signedBy(t *testing.T, token string, data []byte, want ssh.PublicKey) bool {
	t.Helper()
	i := strings.LastIndex(token, ":")
	if i < 0 {
		t.Fatalf("token %q has no signature", token)
	}
	wantPub := bytes.TrimSpace(bytes.Split(ssh.MarshalAuthorizedKey(want), []byte(" "))[1])
	if token[:i] != string(wantPub) {
		return false
	}
	blob, err := base64.StdEncoding.DecodeString(token[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	return want.Verify(data, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: blob}) == nil
}

func TestARequestSignsWithItsAccountsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	primary := writeKey(t, filepath.Join(home, ".ollama", defaultPrivateKey))
	accountPath := filepath.Join(home, ".ollama", "xollama-cloud", "0a1b2c3d", "id_ed25519")
	account := writeKey(t, accountPath)
	data := []byte("POST,/api/chat?ts=1")

	tok, err := Sign(WithKeyFile(context.Background(), accountPath), data)
	if err != nil {
		t.Fatal(err)
	}
	if !signedBy(t, tok, data, account) {
		t.Fatal("a request carrying an account's key was not signed by that key")
	}

	// Off means off: no account on the context signs with the default key.
	tok, err = Sign(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if !signedBy(t, tok, data, primary) {
		t.Fatal("a request without an account was not signed by ~/.ollama/id_ed25519")
	}
}
