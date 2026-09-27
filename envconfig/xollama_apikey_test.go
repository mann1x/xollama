package envconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apiKeyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XOLLAMA_API_KEY", "")
	t.Setenv("OLLAMA_API_KEY", "")
	os.MkdirAll(filepath.Join(home, ".ollama"), 0o755)
	return home
}

func TestTheServerKeyComesFromTheEnvironmentFirstThenTheFile(t *testing.T) {
	home := apiKeyHome(t)
	if _, src := ServerAPIKey(); src != "" {
		t.Fatalf("no key: source = %q", src)
	}
	os.WriteFile(filepath.Join(home, ".ollama", ServerKeyFile), ServerKeyFileContent("file-key-aaaaaaaaaa"), 0o600)
	if d, src := ServerAPIKey(); src != "file" || d != KeyDigest("file-key-aaaaaaaaaa") {
		t.Fatalf("file: source = %q", src)
	}
	t.Setenv("XOLLAMA_API_KEY", "env-key-aaaaaaaaaa")
	if d, src := ServerAPIKey(); src != "env" || d != KeyDigest("env-key-aaaaaaaaaa") {
		t.Fatalf("env: source = %q", src)
	}
}

func TestAnOllamaComKeyIsNeverTheLocalKey(t *testing.T) {
	apiKeyHome(t)
	t.Setenv("OLLAMA_API_KEY", "ollama-cloud-key")
	if _, src := ServerAPIKey(); src != "" {
		t.Fatalf("OLLAMA_API_KEY became the local key (source %q)", src)
	}
	if ClientAPIKey() != "" {
		t.Fatalf("OLLAMA_API_KEY is sent as the local key")
	}
}

func TestAMalformedKeyFileDoesNotLockTheServer(t *testing.T) {
	home := apiKeyHome(t)
	for _, body := range []string{"", "{", `{"api_key_sha256":"zz"}`, `{"api_key_sha256":"abcd"}`} {
		os.WriteFile(filepath.Join(home, ".ollama", ServerKeyFile), []byte(body), 0o600)
		if _, src := ServerAPIKey(); src != "" {
			t.Fatalf("%q: source = %q, want none", body, src)
		}
	}
}

func TestTheKeyIsMaskedInTheSettingsDump(t *testing.T) {
	apiKeyHome(t)
	t.Setenv("XOLLAMA_API_KEY", "secret-aaaaaaaaaa")
	for k, v := range AsMap() {
		if strings.Contains(fmt.Sprint(v.Value), "secret-aaaaaaaaaa") {
			t.Fatalf("%s shows the key", k)
		}
	}
	if v := AsMap()["XOLLAMA_API_KEY"].Value; v != "(set)" {
		t.Fatalf("XOLLAMA_API_KEY = %v, want (set)", v)
	}
}
