package tweak

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
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
