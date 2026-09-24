package engine

import "testing"

// The payload hook once hashed /usr/bin/sh -- "cannot identify the engine
// artifact ... artifact=sh" on every opencoti load on Linux -- and so never
// isolated the engine's payload at all.
func TestTheArtifactOfALaunchIsTheEngineNotTheShell(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		name, args := Command("/usr/local/lib/ollama/opencoti-x", []string{"--port", "1"}, nil, goos)
		if got := ArtifactOf(name, args); got != "/usr/local/lib/ollama/opencoti-x" {
			t.Errorf("%s: ArtifactOf(%q, %q) = %q", goos, name, args, got)
		}
	}
	if got := ArtifactOf("/lib/ollama/llama-server", []string{"--port", "1"}); got != "/lib/ollama/llama-server" {
		t.Errorf("the stock server is its own artifact, got %q", got)
	}
}
