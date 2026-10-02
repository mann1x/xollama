package envconfig

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// withSettings points the server's home at a temporary directory holding
// content as its settings file ("" for no file).
func withSettings(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	setTestHome(t, home)
	if content != "" {
		dir := filepath.Join(home, ".ollama")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SettingsFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ReloadSettings()
	t.Cleanup(ReloadSettings)
}

func TestNoSettingsFileLeavesTheEnvironmentAlone(t *testing.T) {
	withSettings(t, "")
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "q4_0")
	t.Setenv("XOLLAMA_KV_CACHE_TYPE", "")
	if got := Var("OLLAMA_KV_CACHE_TYPE"); got != "q4_0" {
		t.Fatalf("Var = %q, want the environment's q4_0", got)
	}
	if got, want := Environ(), os.Environ(); !slices.Equal(got, want) {
		t.Fatal("Environ differs from os.Environ with no settings file")
	}
}

func TestTheTweakFileBeatsTheEnvironment(t *testing.T) {
	withSettings(t, `{"envs": {"OLLAMA_KV_CACHE_TYPE": "q8_0"}}`)
	// Under either spelling, the environment loses: the owner's ruling.
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "q4_0")
	t.Setenv("XOLLAMA_KV_CACHE_TYPE", "f16")
	if got := Var("OLLAMA_KV_CACHE_TYPE"); got != "q8_0" {
		t.Fatalf("Var = %q, want the tweak file's q8_0", got)
	}
	src := LookupSource("OLLAMA_KV_CACHE_TYPE")
	if !src.HasTweak || src.Value != "q8_0" || src.Env != "f16" {
		t.Fatalf("LookupSource = %+v, want tweak q8_0 over env f16", src)
	}
}

func TestTheXollamaSpellingWinsInsideTheTweakFile(t *testing.T) {
	withSettings(t, `{"envs": {"OLLAMA_KV_CACHE_TYPE": "q4_0", "XOLLAMA_KV_CACHE_TYPE": "q8_0"}}`)
	if got := Var("OLLAMA_KV_CACHE_TYPE"); got != "q8_0" {
		t.Fatalf("Var = %q, want q8_0", got)
	}
}

func TestTheListenAddressTakesOnlyTheXollamaOverride(t *testing.T) {
	withSettings(t, `{"envs": {"OLLAMA_HOST": "0.0.0.0:11434"}}`)
	t.Setenv("XOLLAMA_HOST", "")
	if got := XollamaOnly("OLLAMA_HOST"); got != "" {
		t.Fatalf("XollamaOnly = %q: a stock ollama's address must not steer xollama", got)
	}
	withSettings(t, `{"envs": {"XOLLAMA_HOST": "0.0.0.0:22500"}}`)
	if got := Host().Host; got != "0.0.0.0:22500" {
		t.Fatalf("Host = %q, want the tweak file's 0.0.0.0:22500", got)
	}
}

func TestTheEngineSeesTheOverrides(t *testing.T) {
	withSettings(t, `{"envs": {"OPENCOTI_LINK_GBPS": "25.6", "LLAMA_ARG_FIT": "off"}}`)
	t.Setenv("LLAMA_ARG_FIT", "on")
	env := Environ()
	if !slices.Contains(env, "OPENCOTI_LINK_GBPS=25.6") {
		t.Fatal("an override the environment lacks is not added")
	}
	if !slices.Contains(env, "LLAMA_ARG_FIT=off") || slices.Contains(env, "LLAMA_ARG_FIT=on") {
		t.Fatal("an override does not replace the environment's value")
	}
	if v, ok := LookupEnv("OPENCOTI_LINK_GBPS"); !ok || v != "25.6" {
		t.Fatalf("LookupEnv = %q %v", v, ok)
	}
}

func TestOtherSectionsSurviveARoundTrip(t *testing.T) {
	var s Settings
	in := `{"envs":{"A":"1"},"gpu":{"split":"spread"}}`
	if err := s.UnmarshalJSON([]byte(in)); err != nil {
		t.Fatal(err)
	}
	data, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := back.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	if back.Envs["A"] != "1" || string(back.Rest["gpu"]) == "" {
		t.Fatalf("round trip lost a section: %s", data)
	}
}

func TestABrokenSettingsFileOverridesNothing(t *testing.T) {
	withSettings(t, `{"envs": `)
	t.Setenv("OLLAMA_KV_CACHE_TYPE", "q4_0")
	t.Setenv("XOLLAMA_KV_CACHE_TYPE", "")
	if got := Var("OLLAMA_KV_CACHE_TYPE"); got != "q4_0" {
		t.Fatalf("Var = %q, want the environment's", got)
	}
}

func TestWhatTheTweakFileMaySet(t *testing.T) {
	for _, name := range []string{"OLLAMA_KV_CACHE_TYPE", "XOLLAMA_KV_CACHE_TYPE", "XOLLAMA_ENGINE", "LLAMA_ARG_FIT", "OPENCOTI_LINK_GBPS", "CUDA_VISIBLE_DEVICES"} {
		if err := CheckOverride(name); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for _, name := range []string{"XOLLAMA_API_KEY", "OLLAMA_API_KEY", "PATH", "A=B", "", "XOLLAMA_NOT_A_THING"} {
		if CheckOverride(name) == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestWhatWaitsForARestart(t *testing.T) {
	for name, want := range map[string]bool{
		"XOLLAMA_HOST": true, "OLLAMA_MODELS": true, "XOLLAMA_MODELS": true, "CUDA_VISIBLE_DEVICES": true,
		"OLLAMA_KV_CACHE_TYPE": false, "XOLLAMA_MAX_PARALLEL": false, "LLAMA_ARG_FIT": false,
	} {
		if got := NeedsRestart(name); got != want {
			t.Errorf("NeedsRestart(%s) = %v, want %v", name, got, want)
		}
	}
}
