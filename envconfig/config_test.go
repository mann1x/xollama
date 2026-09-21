package envconfig

import (
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/ollama/ollama/logutil"
)

// TestTheDefaultPortIsNotUpstreams pins the number itself. Every other test
// here now refers to DefaultPort and would follow it anywhere -- including back
// to 11434, which is the one value it must never hold: a fork bound to the port
// IANA registered to "ollama" cannot run beside a stock ollama, and every
// measurement this repo publishes is a comparison that needs both up at once.
// TestTheListenAddressIsNotInheritedFromAStockOllama is the guarantee the
// separate default would otherwise only imply.
//
// OLLAMA_HOST is the variable someone running a stock ollama exports, often
// machine-wide. If xollama read it, the default port would protect nothing:
// the moment that variable existed, both servers would claim one socket and
// the second to start would fail to bind. So the listen address is the one
// setting that is namespaced exclusively.
//
// Deleting the XollamaOnly call in Host() and going back to Var() fails here
// and nowhere else -- every other host test sets XOLLAMA_HOST, which Var()
// would also honour.
func TestTheListenAddressIsNotInheritedFromAStockOllama(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "0.0.0.0:11434")
	t.Setenv("XOLLAMA_HOST", "")
	if got := Host().String(); got != "http://127.0.0.1:"+DefaultPort {
		t.Errorf("Host() = %q; a stock ollama's OLLAMA_HOST must not move xollama's listener", got)
	}

	// ...and the xollama spelling still steers it.
	t.Setenv("XOLLAMA_HOST", "0.0.0.0:31000")
	if got := Host().String(); got != "http://0.0.0.0:31000" {
		t.Errorf("Host() = %q, want http://0.0.0.0:31000", got)
	}
}

// Other settings are NOT exclusive, and must not become so: two installations
// can share a models directory or a cache type without fighting over anything.
func TestOnlyTheListenAddressIsExclusive(t *testing.T) {
	t.Setenv("OLLAMA_FLASH_ATTENTION", "1")
	t.Setenv("XOLLAMA_FLASH_ATTENTION", "")
	if !FlashAttention(false) {
		t.Error("OLLAMA_FLASH_ATTENTION stopped being honoured; only the listen address is exclusive")
	}
}

func TestTheDefaultPortIsNotUpstreams(t *testing.T) {
	if DefaultPort == "11434" {
		t.Fatal("DefaultPort is upstream's port; xollama could not run beside a stock ollama, which is what every A/B in this repo requires")
	}
	if DefaultPort != "22434" {
		t.Errorf("DefaultPort = %q, want 22434 (11434 is IANA-registered to ollama; 22400-22499 is unassigned)", DefaultPort)
	}

	t.Setenv("XOLLAMA_HOST", "")
	if got := Host().String(); got != "http://127.0.0.1:22434" {
		t.Errorf("Host() with nothing set = %q, want http://127.0.0.1:22434", got)
	}
}

func TestHost(t *testing.T) {
	cases := map[string]struct {
		value  string
		expect string
	}{
		"empty":               {"", "http://127.0.0.1:" + DefaultPort},
		"only address":        {"1.2.3.4", "http://1.2.3.4:" + DefaultPort},
		"only port":           {":1234", "http://:1234"},
		"address and port":    {"1.2.3.4:1234", "http://1.2.3.4:1234"},
		"hostname":            {"example.com", "http://example.com:" + DefaultPort},
		"hostname and port":   {"example.com:1234", "http://example.com:1234"},
		"zero port":           {":0", "http://:0"},
		"too large port":      {":66000", "http://:" + DefaultPort},
		"too small port":      {":-1", "http://:" + DefaultPort},
		"ipv6 localhost":      {"[::1]", "http://[::1]:" + DefaultPort},
		"ipv6 world open":     {"[::]", "http://[::]:" + DefaultPort},
		"ipv6 no brackets":    {"::1", "http://[::1]:" + DefaultPort},
		"ipv6 + port":         {"[::1]:1337", "http://[::1]:1337"},
		"extra space":         {" 1.2.3.4 ", "http://1.2.3.4:" + DefaultPort},
		"extra quotes":        {"\"1.2.3.4\"", "http://1.2.3.4:" + DefaultPort},
		"extra space+quotes":  {" \" 1.2.3.4 \" ", "http://1.2.3.4:" + DefaultPort},
		"extra single quotes": {"'1.2.3.4'", "http://1.2.3.4:" + DefaultPort},
		"http":                {"http://1.2.3.4", "http://1.2.3.4:80"},
		"http port":           {"http://1.2.3.4:4321", "http://1.2.3.4:4321"},
		"https":               {"https://1.2.3.4", "https://1.2.3.4:443"},
		"https port":          {"https://1.2.3.4:4321", "https://1.2.3.4:4321"},
		"proxy path":          {"https://example.com/ollama", "https://example.com:443/ollama"},
		"ollama.com":          {"ollama.com", "https://ollama.com:443"},
	}

	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XOLLAMA_HOST", tt.value)
			if host := Host(); host.String() != tt.expect {
				t.Errorf("%s: expected %s, got %s", name, tt.expect, host.String())
			}
		})
	}
}

func TestConnectableHost(t *testing.T) {
	cases := map[string]struct {
		value  string
		expect string
	}{
		"empty":                    {"", "http://127.0.0.1:" + DefaultPort},
		"localhost":                {"127.0.0.1", "http://127.0.0.1:" + DefaultPort},
		"localhost and port":       {"127.0.0.1:1234", "http://127.0.0.1:1234"},
		"ipv4 unspecified":         {"0.0.0.0", "http://127.0.0.1:" + DefaultPort},
		"ipv4 unspecified + port":  {"0.0.0.0:1234", "http://127.0.0.1:1234"},
		"ipv6 unspecified":         {"[::]", "http://[::1]:" + DefaultPort},
		"ipv6 unspecified + port":  {"[::]:1234", "http://[::1]:1234"},
		"ipv6 localhost":           {"[::1]", "http://[::1]:" + DefaultPort},
		"ipv6 localhost + port":    {"[::1]:1234", "http://[::1]:1234"},
		"specific address":         {"192.168.1.5", "http://192.168.1.5:" + DefaultPort},
		"specific address + port":  {"192.168.1.5:8080", "http://192.168.1.5:8080"},
		"hostname":                 {"example.com", "http://example.com:" + DefaultPort},
		"hostname and port":        {"example.com:1234", "http://example.com:1234"},
		"https unspecified + port": {"https://0.0.0.0:4321", "https://127.0.0.1:4321"},
	}

	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XOLLAMA_HOST", tt.value)
			if host := ConnectableHost(); host.String() != tt.expect {
				t.Errorf("%s: expected %s, got %s", name, tt.expect, host.String())
			}
		})
	}
}

func TestOrigins(t *testing.T) {
	cases := []struct {
		value  string
		expect []string
	}{
		{"", []string{
			"http://localhost",
			"https://localhost",
			"http://localhost:*",
			"https://localhost:*",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://127.0.0.1:*",
			"https://127.0.0.1:*",
			"http://0.0.0.0",
			"https://0.0.0.0",
			"http://0.0.0.0:*",
			"https://0.0.0.0:*",
			"app://*",
			"file://*",
			"tauri://*",
			"vscode-webview://*",
			"vscode-file://*",
		}},
		{"http://10.0.0.1", []string{
			"http://10.0.0.1",
			"http://localhost",
			"https://localhost",
			"http://localhost:*",
			"https://localhost:*",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://127.0.0.1:*",
			"https://127.0.0.1:*",
			"http://0.0.0.0",
			"https://0.0.0.0",
			"http://0.0.0.0:*",
			"https://0.0.0.0:*",
			"app://*",
			"file://*",
			"tauri://*",
			"vscode-webview://*",
			"vscode-file://*",
		}},
		{"http://172.16.0.1,https://192.168.0.1", []string{
			"http://172.16.0.1",
			"https://192.168.0.1",
			"http://localhost",
			"https://localhost",
			"http://localhost:*",
			"https://localhost:*",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://127.0.0.1:*",
			"https://127.0.0.1:*",
			"http://0.0.0.0",
			"https://0.0.0.0",
			"http://0.0.0.0:*",
			"https://0.0.0.0:*",
			"app://*",
			"file://*",
			"tauri://*",
			"vscode-webview://*",
			"vscode-file://*",
		}},
		{"http://totally.safe,http://definitely.legit", []string{
			"http://totally.safe",
			"http://definitely.legit",
			"http://localhost",
			"https://localhost",
			"http://localhost:*",
			"https://localhost:*",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://127.0.0.1:*",
			"https://127.0.0.1:*",
			"http://0.0.0.0",
			"https://0.0.0.0",
			"http://0.0.0.0:*",
			"https://0.0.0.0:*",
			"app://*",
			"file://*",
			"tauri://*",
			"vscode-webview://*",
			"vscode-file://*",
		}},
	}
	for _, tt := range cases {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("OLLAMA_ORIGINS", tt.value)

			if diff := cmp.Diff(AllowedOrigins(), tt.expect); diff != "" {
				t.Errorf("%s: mismatch (-want +got):\n%s", tt.value, diff)
			}
		})
	}
}

func TestBool(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"true":  true,
		"false": false,
		"1":     true,
		"0":     false,
		// invalid values
		"random":    true,
		"something": true,
	}

	for k, v := range cases {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OLLAMA_BOOL", k)
			if b := Bool("OLLAMA_BOOL")(); b != v {
				t.Errorf("%s: expected %t, got %t", k, v, b)
			}
		})
	}
}

func TestUint(t *testing.T) {
	cases := map[string]uint{
		"0":    0,
		"1":    1,
		"1337": 1337,
		// default values
		"":       11434,
		"-1":     11434,
		"0o10":   11434,
		"0x10":   11434,
		"string": 11434,
	}

	for k, v := range cases {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OLLAMA_UINT", k)
			if i := Uint("OLLAMA_UINT", 11434)(); i != v {
				t.Errorf("%s: expected %d, got %d", k, v, i)
			}
		})
	}
}

func TestKeepAlive(t *testing.T) {
	cases := map[string]time.Duration{
		"":       5 * time.Minute,
		"1s":     time.Second,
		"1m":     time.Minute,
		"1h":     time.Hour,
		"5m0s":   5 * time.Minute,
		"1h2m3s": 1*time.Hour + 2*time.Minute + 3*time.Second,
		"0":      time.Duration(0),
		"60":     60 * time.Second,
		"120":    2 * time.Minute,
		"3600":   time.Hour,
		"-0":     time.Duration(0),
		"-1":     time.Duration(math.MaxInt64),
		"-1m":    time.Duration(math.MaxInt64),
		// invalid values
		" ":   5 * time.Minute,
		"???": 5 * time.Minute,
		"1d":  5 * time.Minute,
		"1y":  5 * time.Minute,
		"1w":  5 * time.Minute,
	}

	for tt, expect := range cases {
		t.Run(tt, func(t *testing.T) {
			t.Setenv("OLLAMA_KEEP_ALIVE", tt)
			if actual := KeepAlive(); actual != expect {
				t.Errorf("%s: expected %s, got %s", tt, expect, actual)
			}
		})
	}
}

func TestLoadTimeout(t *testing.T) {
	defaultTimeout := 5 * time.Minute
	cases := map[string]time.Duration{
		"":       defaultTimeout,
		"1s":     time.Second,
		"1m":     time.Minute,
		"1h":     time.Hour,
		"5m0s":   defaultTimeout,
		"1h2m3s": 1*time.Hour + 2*time.Minute + 3*time.Second,
		"0":      time.Duration(math.MaxInt64),
		"60":     60 * time.Second,
		"120":    2 * time.Minute,
		"3600":   time.Hour,
		"-0":     time.Duration(math.MaxInt64),
		"-1":     time.Duration(math.MaxInt64),
		"-1m":    time.Duration(math.MaxInt64),
		// invalid values
		" ":   defaultTimeout,
		"???": defaultTimeout,
		"1d":  defaultTimeout,
		"1y":  defaultTimeout,
		"1w":  defaultTimeout,
	}

	for tt, expect := range cases {
		t.Run(tt, func(t *testing.T) {
			t.Setenv("OLLAMA_LOAD_TIMEOUT", tt)
			if actual := LoadTimeout(); actual != expect {
				t.Errorf("%s: expected %s, got %s", tt, expect, actual)
			}
		})
	}
}

func TestVar(t *testing.T) {
	cases := map[string]string{
		"value":       "value",
		" value ":     "value",
		" 'value' ":   "value",
		` "value" `:   "value",
		" ' value ' ": " value ",
		` " value " `: " value ",
	}

	for k, v := range cases {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OLLAMA_VAR", k)
			if s := Var("OLLAMA_VAR"); s != v {
				t.Errorf("%s: expected %q, got %q", k, v, s)
			}
		})
	}
}

func TestContextLength(t *testing.T) {
	cases := map[string]uint{
		"":     0,
		"2048": 2048,
	}

	for k, v := range cases {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OLLAMA_CONTEXT_LENGTH", k)
			if i := ContextLength(); i != v {
				t.Errorf("%s: expected %d, got %d", k, v, i)
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		// Default to INFO
		"":      slog.LevelInfo,
		"false": slog.LevelInfo,
		"f":     slog.LevelInfo,
		"0":     slog.LevelInfo,

		// True values enable Debug
		"true": slog.LevelDebug,
		"t":    slog.LevelDebug,

		// Positive values increase verbosity
		"1": slog.LevelDebug,
		"2": logutil.LevelTrace,

		// Negative values decrease verbosity
		"-1": slog.LevelWarn,
		"-2": slog.LevelError,
	}

	for k, v := range cases {
		t.Run(k, func(t *testing.T) {
			t.Setenv("OLLAMA_DEBUG", k)
			if i := LogLevel(); i != v {
				t.Errorf("%s: expected %d, got %d", k, v, i)
			}
		})
	}
}

func TestNoCloud(t *testing.T) {
	tests := []struct {
		name          string
		envValue      string
		configContent string
		wantDisabled  bool
		wantSource    string
	}{
		{
			name:         "neither env nor config",
			wantDisabled: false,
			wantSource:   "none",
		},
		{
			name:         "env only",
			envValue:     "1",
			wantDisabled: true,
			wantSource:   "env",
		},
		{
			name:          "config only",
			configContent: `{"disable_ollama_cloud": true}`,
			wantDisabled:  true,
			wantSource:    "config",
		},
		{
			name:          "both env and config",
			envValue:      "1",
			configContent: `{"disable_ollama_cloud": true}`,
			wantDisabled:  true,
			wantSource:    "both",
		},
		{
			name:          "config false",
			configContent: `{"disable_ollama_cloud": false}`,
			wantDisabled:  false,
			wantSource:    "none",
		},
		{
			name:          "invalid config ignored",
			configContent: `{invalid json`,
			wantDisabled:  false,
			wantSource:    "none",
		},
		{
			name:         "no config file",
			wantDisabled: false,
			wantSource:   "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.configContent != "" {
				configDir := filepath.Join(home, ".ollama")
				if err := os.MkdirAll(configDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "server.json"), []byte(tt.configContent), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			setTestHome(t, home)
			t.Setenv("OLLAMA_NO_CLOUD", tt.envValue)

			if got := NoCloud(); got != tt.wantDisabled {
				t.Errorf("NoCloud() = %v, want %v", got, tt.wantDisabled)
			}

			if got := NoCloudSource(); got != tt.wantSource {
				t.Errorf("NoCloudSource() = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

func TestXollamaKey(t *testing.T) {
	cases := map[string]string{
		// Upstream's namespace is replaced, not stacked.
		"OLLAMA_HOST":  "XOLLAMA_HOST",
		"OLLAMA_DEBUG": "XOLLAMA_DEBUG",
		"OLLAMA_":      "XOLLAMA_",

		// Already ours: nothing overrides it.
		"XOLLAMA_ENGINE": "",

		// Somebody else's variable, read by them under this exact name. An
		// XOLLAMA_ spelling would work on our side of the process boundary and
		// be ignored on theirs, so there is none.
		"LLAMA_ARG_FIT":            "",
		"LLAMA_ARG_FIT_TARGET":     "",
		"HSA_OVERRIDE_GFX_VERSION": "",
		"CUDA_VISIBLE_DEVICES":     "",
		"HTTP_PROXY":               "",
		"http_proxy":               "",
		"NOT_OLLAMA_HOST":          "",
	}

	for k, want := range cases {
		t.Run(k, func(t *testing.T) {
			if got := XollamaKey(k); got != want {
				t.Errorf("XollamaKey(%q) = %q, want %q", k, got, want)
			}
		})
	}
}

func TestVarPrefersXollamaSpelling(t *testing.T) {
	t.Run("xollama wins", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", "stock")
		t.Setenv("XOLLAMA_MODELS", "fork")
		if got := Var("OLLAMA_MODELS"); got != "fork" {
			t.Errorf("got %q, want %q", got, "fork")
		}
	})

	t.Run("falls back to ollama", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", "stock")
		t.Setenv("XOLLAMA_MODELS", "")
		if got := Var("OLLAMA_MODELS"); got != "stock" {
			t.Errorf("got %q, want %q", got, "stock")
		}
	})

	t.Run("empty does not mask", func(t *testing.T) {
		// Every caller here already treats "" as unset, so an empty XOLLAMA_
		// value must not shadow a set OLLAMA_ one -- there would be no way to
		// tell the two apart downstream.
		t.Setenv("OLLAMA_FLASH_ATTENTION", "1")
		t.Setenv("XOLLAMA_FLASH_ATTENTION", "   ")
		if !FlashAttention(false) {
			t.Error("blank XOLLAMA_FLASH_ATTENTION masked OLLAMA_FLASH_ATTENTION=1")
		}
	})

	t.Run("trims the xollama spelling too", func(t *testing.T) {
		t.Setenv("XOLLAMA_MODELS", ` "/models" `)
		if got := Var("OLLAMA_MODELS"); got != "/models" {
			t.Errorf("got %q, want %q", got, "/models")
		}
	})

	t.Run("no xollama spelling for a vendor variable", func(t *testing.T) {
		// llama-server reads LLAMA_ARG_FIT itself, from the name it inherits.
		t.Setenv("LLAMA_ARG_FIT", "off")
		t.Setenv("XOLLAMA_LLAMA_ARG_FIT", "on")
		if got := Var("LLAMA_ARG_FIT"); got != "off" {
			t.Errorf("got %q, want %q", got, "off")
		}
	})

	t.Run("an already-xollama key is read as itself", func(t *testing.T) {
		t.Setenv("XOLLAMA_ENGINE", "opencoti")
		if got := Var("XOLLAMA_ENGINE"); got != "opencoti" {
			t.Errorf("got %q, want %q", got, "opencoti")
		}
	})
}

func TestAsMapNamesAreBrandedButKeysAreNot(t *testing.T) {
	for key, v := range AsMap() {
		want := key
		if x := XollamaKey(key); x != "" {
			want = x
		}
		if v.Name != want {
			t.Errorf("AsMap()[%q].Name = %q, want %q", key, v.Name, want)
		}
	}

	// cmd/cmd.go and discover/runner.go index this map by the upstream
	// spelling; branding the keys would silently empty both.
	for _, key := range []string{"OLLAMA_HOST", "OLLAMA_DEBUG", "OLLAMA_MODELS"} {
		if _, ok := AsMap()[key]; !ok {
			t.Errorf("AsMap() lost key %q", key)
		}
	}
}
