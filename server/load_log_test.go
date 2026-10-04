package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ollama/ollama/api"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

func attrs(l loadLogLine) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(l.attrs); i += 2 {
		out[fmt.Sprint(l.attrs[i])] = fmt.Sprint(l.attrs[i+1])
	}
	return out
}

// A load names the model and its tag, what the file is, and where it goes,
// from what the scheduler already has, so a log line from the engine that
// names only a blob can be read against it.
func TestALoadIsLoggedByTheModelsName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sha256-0123")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gguftest.Write(file, gguftest.KV{
		"general.architecture":              "qwen3moe",
		"general.file_type":                 uint32(15), // Q4_K_M
		"qwen3moe.block_count":              uint32(48),
		"qwen3moe.embedding_length":         uint32(2048),
		"qwen3moe.context_length":           uint32(262144),
		"qwen3moe.attention.head_count":     uint32(32),
		"qwen3moe.attention.head_count_kv":  uint32(4),
		"qwen3moe.expert_count":             uint32(128),
		"qwen3moe.expert_used_count":        uint32(8),
		"qwen3moe.attention.sliding_window": uint32(4096),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := llm.LoadModel(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	m := &Model{ShortName: "qwen3:30b-a3b", Name: "registry.ollama.ai/library/qwen3:30b-a3b", ModelPath: path, ProjectorPaths: []string{"p"}}
	opts := api.DefaultOptions()
	opts.NumCtx, opts.NumBatch = 32768, 512
	gpus := []ml.DeviceInfo{{DeviceID: ml.DeviceID{Library: "CUDA", ID: "0"}, Description: "NVIDIA GeForce RTX 3090"}}

	lines := modelLoadLog(m, f, gpus, opts, 2, 20<<30)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	want := []map[string]string{
		{"model": "qwen3:30b-a3b", "blob": "sha256-0123", "projectors": "1"},
		{
			"model": "qwen3:30b-a3b", "family": "qwen3moe", "quant": "Q4_K_M", "layers": "48", "embedding": "2048",
			"heads": "32/4", "experts": "8/128", "sliding_window": "4096", "trained_ctx": "262144",
		},
		{
			"model": "qwen3:30b-a3b", "devices": "CUDA0 NVIDIA GeForce RTX 3090", "num_ctx": "32768", "parallel": "2",
			"num_batch": "512", "predicted": "20.0 GiB",
		},
	}
	for i, w := range want {
		got := attrs(lines[i])
		for k, v := range w {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q (line %v)", lines[i].msg, k, got[k], v, lines[i].attrs)
			}
		}
	}
	if _, ok := attrs(lines[0])["drafter"]; ok {
		t.Error("a model without a drafter logged one")
	}
}

// Without a parsed file there is still a name and a placement, and a CPU load
// says so rather than listing nothing.
func TestALoadWithoutAFileStillNamesTheModel(t *testing.T) {
	lines := modelLoadLog(&Model{Name: "x:latest", ModelPath: "/b/sha256-9"}, nil, nil, api.DefaultOptions(), 1, 0)
	if len(lines) != 2 || attrs(lines[0])["model"] != "x:latest" || attrs(lines[1])["devices"] != "cpu" {
		t.Fatalf("lines = %+v", lines)
	}
}
