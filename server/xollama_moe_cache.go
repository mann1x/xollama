package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/model"
)

// xollama-hook: moe-cache
//
// The MoE expert cache (opencoti c14 r19): `ps` lists the engine's record of
// it (moe_cache_state_v1), and POST /api/xollama/moe-cache/forget deletes a
// model's warm-start profile. See docs/xollama/moe-cache.mdx.

// moeCacheReporter is a runner whose engine keeps a MoE cache record.
type moeCacheReporter interface {
	MoECacheState(context.Context) []api.MoECacheDevice
}

// psMoECache is the record `ps` lists for a runner, or nil.
func psMoECache(ctx context.Context, llama llm.LlamaServer) []api.MoECacheDevice {
	r, ok := llama.(moeCacheReporter)
	if !ok || llama == nil {
		return nil
	}
	return r.MoECacheState(ctx)
}

// moeProfileKey is the engine's warm-start profile key for a GGUF: FNV-1a 64
// (with the engine's offset basis, moeKeyOffset) over general.name ("n/a" when absent, as llama.cpp names it), then the
// layer count, the expert count and the model's element count as three
// little-endian u64 (opencoti llama-moe-cache.cpp profile_init; checked
// against the file the r19 engine wrote, a model with a NextN layer
// included). ok is false for a model with no experts.
func moeProfileKey(path string) (key string, ok bool, err error) {
	f, err := gguf.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	// KeyValue prefixes the architecture to every key outside general.
	experts := f.KeyValue("expert_count").Uint()
	if experts == 0 {
		return "", false, nil
	}
	name := f.KeyValue("general.name").String()
	if name == "" {
		name = "n/a"
	}
	var elements uint64
	for _, t := range f.TensorInfos() {
		n := t.NumValues()
		if n < 0 {
			return "", false, fmt.Errorf("tensor %s: element count overflows", t.Name)
		}
		elements += uint64(n)
	}
	h := moeKeyHash{h: moeKeyOffset}
	h.Write([]byte(name))
	var dims [24]byte
	binary.LittleEndian.PutUint64(dims[0:], f.KeyValue("block_count").Uint())
	binary.LittleEndian.PutUint64(dims[8:], experts)
	binary.LittleEndian.PutUint64(dims[16:], elements)
	h.Write(dims[:])
	return fmt.Sprintf("%016x", h.h), true, nil
}

// moeKeyOffset is the engine's FNV-1a offset basis as written in its source,
// 1469598103934665603: the standard basis (14695981039346656037) without its
// last digit. hash/fnv uses the standard one and gives another key, so the
// loop is written out. Checked against the file the r19 engine wrote
// (Qwen3.6 27B A3B: f335783fc163c1be).
const moeKeyOffset uint64 = 1469598103934665603

type moeKeyHash struct{ h uint64 }

func (k *moeKeyHash) Write(b []byte) {
	for _, c := range b {
		k.h = (k.h ^ uint64(c)) * 1099511628211
	}
}

// moeProfileDirs are where the engine keeps profiles: under each HOME xollama
// may start it with (engine.DefaultPayloadRoots), never the user's own.
var moeProfileDirs = func() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	for _, root := range engine.DefaultPayloadRoots(ml.LibOllamaPath, home) {
		dirs = append(dirs, filepath.Join(root, ".llamafile", "moe-cache"))
	}
	return dirs
}

// moeUnloadWait bounds the wait for a loaded model to unload before its
// profile is deleted: the engine writes the profile as it exits.
var moeUnloadWait = 30 * time.Second

// MoECacheForgetHandler deletes a model's MoE cache warm-start profile. A
// loaded model is unloaded first, or the engine would write the profile
// again as it exits; one serving a request is refused (409).
func (s *Server) MoECacheForgetHandler(c *gin.Context) {
	var req api.MoECacheForgetRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Model == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	name := model.ParseName(req.Model)
	if !name.IsValid() {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid model name %q", req.Model)})
		return
	}
	m, err := GetModel(name.String())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("model %q not found", req.Model)})
		return
	}
	if m.ModelPath == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s has no GGUF weights, so no MoE cache", req.Model)})
		return
	}
	key, ok, err := moeProfileKey(m.ModelPath)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("reading %s: %v", req.Model, err)})
		return
	}
	if !ok {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is not a mixture-of-experts model, so it has no MoE cache", req.Model)})
		return
	}
	resp := api.MoECacheForgetResponse{Key: key}
	if v, set := envconfig.LookupEnv("OPENCOTI_MOE_CACHE_PROFILE"); set && v != "" && v != "0" {
		resp.Note = fmt.Sprintf("OPENCOTI_MOE_CACHE_PROFILE names a profile file of its own (%s), shared by every model; it was left alone", v)
	}
	unloaded, err := s.unloadForProfile(name.String())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	resp.Unloaded = unloaded
	for _, dir := range moeProfileDirs() {
		p := filepath.Join(dir, key+".mcp")
		switch err := os.Remove(p); {
		case err == nil:
			resp.Removed = append(resp.Removed, p)
		case !errors.Is(err, os.ErrNotExist):
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("removing %s: %v", p, err)})
			return
		}
	}
	slog.Info("forgot the MoE cache warm-start profile", "model", name.String(), "key", key, "removed", resp.Removed, "unloaded", unloaded)
	c.JSON(http.StatusOK, resp)
}

// unloadForProfile unloads every runner of the model and waits until they
// are gone, so the engine has written its profile before it is deleted.
func (s *Server) unloadForProfile(name string) (bool, error) {
	if s == nil || s.sched == nil {
		return false, nil
	}
	runners := s.sched.runnersNamed(name)
	if len(runners) == 0 {
		return false, nil
	}
	for _, r := range runners {
		r.refMu.Lock()
		busy := r.refCount > 0
		r.refMu.Unlock()
		if busy {
			return false, fmt.Errorf("%s is serving a request; forget its profile once it is idle", name)
		}
	}
	s.sched.expireRunnersNamed(name)
	deadline := time.Now().Add(moeUnloadWait)
	for time.Now().Before(deadline) {
		if len(s.sched.runnersNamed(name)) == 0 {
			return true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true, fmt.Errorf("%s did not unload within %s; nothing was removed", name, moeUnloadWait)
}
