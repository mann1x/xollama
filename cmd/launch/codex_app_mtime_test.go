package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A filesystem stamps mtime from a coarser clock than time.Now() reads, so a
// rollout file written immediately after the session start can carry an mtime
// that precedes it -- measured at 4.55ms on tmpfs, and a full second on ext3.
// Skipping on a bare Before(start) drops that file for the life of the session
// and the count stays at zero however many prompts are sent.
func TestCodexAppRequestCountScansFileStampedJustBeforeStart(t *testing.T) {
	setTestHome(t, t.TempDir())

	configPath, err := codexConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(filepath.Dir(configPath), "sessions")
	path := filepath.Join(sessions, "rollout.jsonl")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(codexAppRoutingCatalogPathForConfig(configPath),
		[]byte(`{"models":[{"slug":"qwen3:8b"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	line := func(at time.Time, payload map[string]any, kind string) []byte {
		b, err := json.Marshal(map[string]any{"timestamp": at, "type": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		return append(b, '\n')
	}
	data := line(start.Add(time.Second), map[string]any{"model": "qwen3:8b"}, "turn_context")
	data = append(data, line(start.Add(2*time.Second), map[string]any{
		"type": "message", "role": "user",
		"internal_chat_message_metadata_passthrough": map[string]any{
			"content_item_kinds": []string{"user.text"},
		},
	}, "response_item")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reproduce the coarse-clock gap explicitly rather than relying on the
	// host filesystem to have one: the file's lines are still after start.
	stamp := start.Add(-50 * time.Millisecond)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	cursor := &codexAppRequestCursor{}
	if got := cursor.scan(sessions, start, codexAppRegularProfileRoutingModels(configPath)); got != 1 {
		t.Fatalf("request count = %d, want 1: a file stamped %v before start was skipped",
			got, start.Sub(stamp))
	}
}
