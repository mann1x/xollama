package discover

import (
	"testing"

	"github.com/ollama/ollama/llm/engine"
)

// With XOLLAMA_ENGINE=opencoti every backend is asked for, policy aside. On
// Linux that ran `--gpu apple`, which cannot succeed, and a failed listing is
// a Warn on every start.
func TestABackendIsOnlyListedWhereItCanExist(t *testing.T) {
	for _, tt := range []struct {
		b    engine.Backend
		goos string
		want bool
	}{
		{engine.BackendMetal, "darwin", true},
		{engine.BackendMetal, "linux", false},
		{engine.BackendMetal, "windows", false},
		{engine.BackendCUDA, "linux", true},
		{engine.BackendVulkan, "windows", true},
		{engine.BackendCUDA, "darwin", false},
		{engine.BackendVulkan, "darwin", false},
	} {
		if got := existsOn(tt.b, tt.goos); got != tt.want {
			t.Errorf("existsOn(%s, %s) = %v, want %v", tt.b, tt.goos, got, tt.want)
		}
	}
}
