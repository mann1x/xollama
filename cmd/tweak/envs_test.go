package tweak

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func TestEnvArgumentsSetAndUnset(t *testing.T) {
	cmd := envsCommand(Options{})
	if err := cmd.Flags().Parse([]string{"--unset", "OLLAMA_DEBUG"}); err != nil {
		t.Fatal(err)
	}
	changes, err := envChangesFromArgs(cmd, []string{"OLLAMA_KV_CACHE_TYPE=q8_0", "XOLLAMA_ENGINE_ARGS=--a=b"})
	if err != nil {
		t.Fatal(err)
	}
	if v := changes["OLLAMA_KV_CACHE_TYPE"]; v == nil || *v != "q8_0" {
		t.Fatalf("set: %v", v)
	}
	if v := changes["XOLLAMA_ENGINE_ARGS"]; v == nil || *v != "--a=b" {
		t.Fatal("a value holding '=' was cut")
	}
	if v, ok := changes["OLLAMA_DEBUG"]; !ok || v != nil {
		t.Fatal("--unset is not a removal")
	}
	if _, err := envChangesFromArgs(cmd, []string{"OLLAMA_DEBUG=1"}); err == nil {
		t.Fatal("a variable both set and unset was accepted")
	}
	if _, err := envChangesFromArgs(envsCommand(Options{}), []string{"OLLAMA_DEBUG"}); err == nil {
		t.Fatal("a bare name was accepted as a setting")
	}
}

func TestTheEnvTableShowsWhatAnOverrideReplaces(t *testing.T) {
	tw, env := "q8_0", "q4_0"
	envs := []api.SettingsEnv{
		{Name: "OLLAMA_KV_CACHE_TYPE", Value: tw, Source: "tweak", Tweak: &tw, Env: &env},
		{Name: "OLLAMA_DEBUG"},
	}
	var b bytes.Buffer
	printEnvTable(&b, envs, false)
	out := b.String()
	if !strings.Contains(out, `overrides env "q4_0"`) {
		t.Fatalf("the replaced value is hidden:\n%s", out)
	}
	if strings.Contains(out, "OLLAMA_DEBUG") {
		t.Fatalf("an unset variable is listed without --all:\n%s", out)
	}
	b.Reset()
	printEnvTable(&b, envs, true)
	if !strings.Contains(b.String(), "OLLAMA_DEBUG") || !strings.Contains(b.String(), "default") {
		t.Fatalf("--all leaves out the defaults:\n%s", b.String())
	}
}

func TestAModelsSettingsNameTheirSource(t *testing.T) {
	two := 2
	on := true
	own := &xollama.Config{Slots: &xollama.Slots{Max: two}, Engine: xollama.EngineLlamaCpp}
	def := &xollama.Config{FlashAttention: "on", Slots: &xollama.Slots{Dynamic: &on}, KV: &xollama.KV{ResidencyMode: xollama.ResidencyHead}}
	got := map[string]string{}
	notApplied := false
	for _, r := range modelSourceRows(own, def)[1:] {
		got[r[0]] = r[2]
		if r[2] == "not applied" {
			notApplied = true
		}
	}
	if got["slots.max"] != "model" || got["engine"] != "model" {
		t.Fatalf("the model's own settings are not marked model: %v", got)
	}
	if got["flash_attention"] != "server" || got["slots.dynamic"] != "server" {
		t.Fatalf("defaults are not marked server: %v", got)
	}
	if !notApplied {
		t.Fatal("a default this model cannot act on is not reported")
	}
}

func TestARollingWindowUnderHeadResidencyIsDropped(t *testing.T) {
	f, _ := fieldByName("kv-rolling-window")
	cfg := &xollama.Config{KV: &xollama.KV{ResidencyMode: xollama.ResidencyHead}}
	if why := f.blocked(cfg); !strings.Contains(why, "disables the window") {
		t.Fatalf("blocked = %q", why)
	}
	if err := f.set(cfg, "640k"); err == nil {
		t.Fatal("a size that is not MiB was accepted")
	}
	cfg.KV.ResidencyMode = xollama.ResidencyWindow
	if err := f.set(cfg, "256"); err != nil || cfg.KV.RollingWindow != "256" {
		t.Fatalf("set 256: %v %+v", err, cfg.KV)
	}
}
