package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
)

// moeTestBlob is a small mixture-of-experts GGUF: 3 blocks, 8 experts, two
// tensors of 6 and 4 elements.
func moeTestBlob(t *testing.T, name string, experts uint32) (string, string) {
	t.Helper()
	kv := gguftest.KV{"general.architecture": "qwen35moe", "qwen35moe.block_count": uint32(3), "qwen35moe.expert_count": experts}
	if name != "" {
		kv["general.name"] = name
	}
	return createBinFile(t, kv, []*gguftest.Tensor{
		{Name: "a.weight", Type: gguf.TensorTypeF32, Shape: []uint64{2, 3}, WriterTo: bytes.NewReader(make([]byte, 24))},
		{Name: "b.weight", Type: gguf.TensorTypeF32, Shape: []uint64{4}, WriterTo: bytes.NewReader(make([]byte, 16))},
	})
}

// specKey is opencoti's profile_init, written out from the source, offset
// basis included.
func specKey(name string, layers, experts, elements uint64) string {
	h := uint64(1469598103934665603)
	mix := func(b []byte) {
		for _, c := range b {
			h = (h ^ uint64(c)) * 1099511628211
		}
	}
	mix([]byte(name))
	var b [24]byte
	binary.LittleEndian.PutUint64(b[0:], layers)
	binary.LittleEndian.PutUint64(b[8:], experts)
	binary.LittleEndian.PutUint64(b[16:], elements)
	mix(b[:])
	return fmt.Sprintf("%016x", h)
}

// The file the r19 release engine wrote for Qwen3.6 27B A3B Coder on solidPC
// (2026-11-27): general.name, 41 blocks (a NextN layer among them), 184
// experts, 26213016704 elements.
func TestTheProfileKeyMatchesTheFileTheEngineWrote(t *testing.T) {
	if got := specKey("Qwen3.6 27B A3B Coder", 41, 184, 26213016704); got != "f335783fc163c1be" {
		t.Fatalf("key %s, the engine wrote f335783fc163c1be.mcp", got)
	}
}

func TestTheProfileKeyIsTheEnginesHash(t *testing.T) {
	path, _ := moeTestBlob(t, "Test MoE", 8)
	key, ok, err := moeProfileKey(path)
	if err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if want := specKey("Test MoE", 3, 8, 10); key != want {
		t.Fatalf("key %s, want %s", key, want)
	}
	// llama.cpp names a model without general.name "n/a".
	path, _ = moeTestBlob(t, "", 8)
	if key, _, _ := moeProfileKey(path); key != specKey("n/a", 3, 8, 10) {
		t.Fatalf("unnamed model key %s", key)
	}
	// A dense model has no cache.
	path, _ = moeTestBlob(t, "dense", 0)
	if _, ok, _ := moeProfileKey(path); ok {
		t.Fatal("a model without experts got a profile key")
	}
}

func TestForgettingTheProfileRemovesTheModelsFileOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	t.Setenv("OPENCOTI_MOE_CACHE_PROFILE", "")
	_, digest := moeTestBlob(t, "Test MoE", 8)
	var s Server
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Model: "moe", Files: map[string]string{"m.gguf": digest}, Stream: &stream}); w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	dirs := []string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}
	old := moeProfileDirs
	moeProfileDirs = func() []string { return dirs }
	t.Cleanup(func() { moeProfileDirs = old })
	key := specKey("Test MoE", 3, 8, 10)
	mine := filepath.Join(dirs[1], key+".mcp")
	other := filepath.Join(dirs[1], "0123456789abcdef.mcp")
	for _, p := range []string{mine, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("MCP1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := createRequest(t, s.MoECacheForgetHandler, api.MoECacheForgetRequest{Model: "moe"})
	if w.Code != http.StatusOK {
		t.Fatalf("forget: %d %s", w.Code, w.Body.String())
	}
	var resp api.MoECacheForgetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Key != key || len(resp.Removed) != 1 || resp.Removed[0] != mine {
		t.Fatalf("response %+v", resp)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("the model's profile is still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another model's profile went too")
	}
	// Again: nothing left, still a 200.
	if w := createRequest(t, s.MoECacheForgetHandler, api.MoECacheForgetRequest{Model: "moe"}); w.Code != http.StatusOK {
		t.Fatalf("second forget: %d", w.Code)
	}
	// Unknown model, and a model with no experts.
	if w := createRequest(t, s.MoECacheForgetHandler, api.MoECacheForgetRequest{Model: "nope"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown model: %d", w.Code)
	}
	_, dense := moeTestBlob(t, "dense", 0)
	createRequest(t, s.CreateHandler, api.CreateRequest{Model: "dense", Files: map[string]string{"m.gguf": dense}, Stream: &stream})
	if w := createRequest(t, s.MoECacheForgetHandler, api.MoECacheForgetRequest{Model: "dense"}); w.Code != http.StatusBadRequest {
		t.Fatalf("dense model: %d %s", w.Code, w.Body.String())
	}
}
