package llm

import (
	"testing"

	"github.com/ollama/ollama/api"
)

func TestStockRepeatLastN(t *testing.T) {
	tests := []struct {
		name     string
		n        int
		numCtx   int
		opencoti bool
		want     int
	}{
		{"stock -1 is num_ctx", -1, 32768, false, 32768},
		{"stock other negative is num_ctx", -2, 8192, false, 8192},
		{"stock positive unchanged", 64, 32768, false, 64},
		{"stock zero unchanged", 0, 32768, false, 0},
		{"stock unknown num_ctx is upstream's answer", -1, 0, false, 0},
		{"opencoti resolves -1 itself", -1, 32768, true, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stockRepeatLastN(tt.n, tt.numCtx, tt.opencoti); got != tt.want {
				t.Fatalf("stockRepeatLastN(%d, %d, %v) = %d, want %d", tt.n, tt.numCtx, tt.opencoti, got, tt.want)
			}
		})
	}
}

// The chat path owns its own body, so it is checked through the builder.
func TestChatBodyRepeatLastNOnStockAndOpencoti(t *testing.T) {
	for _, tt := range []struct {
		opencoti bool
		want     int
	}{{false, 4096}, {true, -1}} {
		s := &llamaServerRunner{options: api.Options{Runner: api.Runner{NumCtx: 4096}}, usedOpencoti: tt.opencoti}
		opts := api.DefaultOptions()
		opts.NumCtx = 4096
		opts.RepeatLastN = -1
		body, err := s.llamaServerChatRequest(ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hi"}},
			Options:  &opts,
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := body["repeat_last_n"]; got != tt.want {
			t.Fatalf("opencoti=%v: repeat_last_n = %v, want %d", tt.opencoti, got, tt.want)
		}
	}
}
