package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

var noStream = false

func mediaDigest(t *testing.T, content []byte) string {
	t.Helper()
	l, err := manifest.NewLayer(bytes.NewReader(content), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	return l.Digest
}

func TestMediaCapabilitiesAreNotUpstreamsImage(t *testing.T) {
	m := &Model{Xollama: &xollama.Config{Media: &xollama.Media{
		Image: &xollama.ImageMedia{Model: "x", Edit: "none"},
		STT:   &xollama.STTMedia{Model: "y"},
		TTS:   &xollama.TTSMedia{Model: "z"},
	}}}
	got := mediaCapabilities(m)
	want := []model.Capability{CapabilityImageGeneration, CapabilityTranscription, CapabilitySpeech}
	if !slices.Equal(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	// generate and chat refuse every model carrying upstream's "image".
	if slices.Contains(m.Capabilities(), model.CapabilityImage) {
		t.Fatal("a media model must never report upstream's image capability")
	}
	if mediaCapabilities(&Model{}) != nil {
		t.Fatal("a model without media reports media")
	}
}

func TestTheMediaTwinIsItsOwnRunner(t *testing.T) {
	m := &Model{
		Name: "qwen3:8b", ShortName: "qwen3:8b", Digest: "sha256:abc", ModelPath: "/blobs/llm",
		Xollama: &xollama.Config{Version: 7, Engine: xollama.EngineOpencoti, Media: &xollama.Media{TTS: &xollama.TTSMedia{Model: "z"}}},
	}
	twin := mediaTwin(m)
	if schedulerModelKey(twin) == schedulerModelKey(m) || !isMediaKey(schedulerModelKey(twin)) {
		t.Fatalf("twin key %q, model key %q", schedulerModelKey(twin), schedulerModelKey(m))
	}
	caps := twin.Capabilities()
	if slices.Contains(caps, model.CapabilityCompletion) {
		t.Fatalf("twin capabilities = %v: the scheduler would treat the media process as an LLM", caps)
	}
	if !slices.Contains(caps, CapabilitySpeech) {
		t.Fatalf("twin capabilities = %v", caps)
	}
	if twin.Xollama.Engine != "" {
		t.Fatal("the twin carries the LLM's launch settings")
	}
}

func TestAMediaOnlyTemplateNeedsNoWeights(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	var s Server
	voice := mediaDigest(t, []byte("kokoro"))

	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model: "kokoro:82m",
		Xollama: &xollama.Config{Media: &xollama.Media{
			TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: voice},
		}},
		Stream: &noStream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	m, err := GetModel("kokoro:82m")
	if err != nil {
		t.Fatal(err)
	}
	caps := m.Capabilities()
	if !slices.Contains(caps, CapabilitySpeech) || slices.Contains(caps, model.CapabilityCompletion) {
		t.Fatalf("capabilities = %v", caps)
	}
	if m.CheckCapabilities(model.CapabilityCompletion) == nil {
		t.Fatal("chat would be scheduled against a template with no LLM")
	}
	show, err := GetModelInfo(api.ShowRequest{Model: "kokoro:82m"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if show.Xollama.Media.TTS.Model != voice || !slices.Contains(show.Capabilities, CapabilitySpeech) {
		t.Fatalf("show = %+v", show)
	}

	// With no media, a create naming neither weights nor a base still fails.
	w = createRequest(t, s.CreateHandler, api.CreateRequest{Model: "empty", Stream: &noStream})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create without a source = %d, want 400", w.Code)
	}
}

func TestAModelIsNotAskedForMediaItDoesNotHave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	var s Server
	voice := mediaDigest(t, []byte("whisper"))
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:   "whisper:turbo",
		Xollama: &xollama.Config{Media: &xollama.Media{STT: &xollama.STTMedia{Model: voice}}},
		Stream:  &noStream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	_, _, err := s.mediaRunner(t.Context(), "whisper:turbo", CapabilitySpeech, nil)
	if !errors.Is(err, errNoMedia) {
		t.Fatalf("err = %v, want errNoMedia", err)
	}
}

// fakeMedia is a media runner with no process behind it.
type fakeMedia struct {
	mockLlm
}

func (f *fakeMedia) MediaDo(context.Context, string, string, url.Values, io.Reader, string) (*http.Response, error) {
	return nil, errors.New("not wired")
}

func (f *fakeMedia) Features() []string { return []string{llm.FeatureSpeech} }

func TestTheSchedulerLoadsAMediaTwinThroughItsOwnConstructor(t *testing.T) {
	ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
	defer done()
	s := InitScheduler(ctx)
	s.newServerFn = func(ml.SystemInfo, []ml.DeviceInfo, string, *gguf.Model, []string, []string, api.Options, int, llm.LlamaServerConfig) (llm.LlamaServer, error) {
		t.Fatal("a media twin reached the LLM constructor")
		return nil, nil
	}
	fake := &fakeMedia{mockLlm{vramSize: 512 << 20, vramByGPU: map[ml.DeviceID]uint64{}}}
	var built *Model
	old := newMediaRunnerFn
	newMediaRunnerFn = func(m *Model) (llm.LlamaServer, error) {
		built = m
		fake.modelPath = m.ModelPath
		return fake, nil
	}
	t.Cleanup(func() { newMediaRunnerFn = old })

	twin := mediaTwin(&Model{
		Name: "kokoro", ShortName: "kokoro", Digest: "sha256:def",
		Xollama: &xollama.Config{Version: 7, Media: &xollama.Media{TTS: &xollama.TTSMedia{Model: "z"}}},
	})
	req := &LlmRequest{
		ctx: ctx, model: twin, opts: api.DefaultOptions(),
		successCh: make(chan *runnerRef, 1), errCh: make(chan error, 1),
		sessionDuration: &api.Duration{Duration: time.Second},
	}
	s.load(req, ml.SystemInfo{}, nil, false)
	select {
	case err := <-req.errCh:
		t.Fatal(err)
	case r := <-req.successCh:
		if built != twin {
			t.Fatal("the constructor was not given the twin")
		}
		if r.modelKey != "media:sha256:def" || r.vramSize != 512<<20 {
			t.Fatalf("runner key %q vram %d", r.modelKey, r.vramSize)
		}
		if _, ok := r.llama.(llm.MediaRunner); !ok {
			t.Fatal("the loaded runner is not a media runner")
		}
	case <-ctx.Done():
		t.Fatal("no runner")
	}
}

func TestMediaPullFetchesTheResolvedBlobFromTheReposRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var s Server
	resolved, downloaded := 0, ""
	oldR, oldD := mediaResolve, mediaDownload
	t.Cleanup(func() { mediaResolve, mediaDownload = oldR, oldD })
	want := "sha256:" + strings.Repeat("c", 64)
	mediaResolve = func(_ context.Context, r mediahub.Ref) (mediahub.File, error) {
		resolved++
		return mediahub.File{Ref: r, Digest: want}, nil
	}
	mediaDownload = func(_ context.Context, registry, digest string, fn func(api.ProgressResponse)) error {
		downloaded = registry + " " + digest
		return nil
	}

	w := createRequest(t, s.MediaPullHandler, api.MediaPullRequest{
		Source: "hf.co/audio-cpp/audio.cpp-gguf/Kokoro-82M-GGUF/kokoro-82m-q8_0.gguf", Stream: &noStream,
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
		t.Fatalf("pull = %d %s", w.Code, w.Body.String())
	}
	if resolved != 1 || downloaded != "hf.co/audio-cpp/audio.cpp-gguf "+want {
		t.Fatalf("resolved %d, downloaded %q", resolved, downloaded)
	}

	// A digest the caller resolved is not asked for again.
	other := "sha256:" + strings.Repeat("d", 64)
	w = createRequest(t, s.MediaPullHandler, api.MediaPullRequest{Source: "hf.co/a/b/x.gguf", Digest: other, Stream: &noStream})
	if w.Code != http.StatusOK || resolved != 1 || downloaded != "hf.co/a/b "+other {
		t.Fatalf("pull = %d, resolved %d, downloaded %q", w.Code, resolved, downloaded)
	}

	for _, bad := range []api.MediaPullRequest{{Source: "/srv/ml/x.gguf"}, {Source: "hf.co/a/b/x.gguf", Digest: "sha256:abc"}} {
		if w := createRequest(t, s.MediaPullHandler, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%+v = %d, want 400", bad, w.Code)
		}
	}
}

func TestAMissingMediaBlobIsRefusedByName(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	present := mediaDigest(t, []byte("GGUF present"))
	gone := "sha256:" + strings.Repeat("e", 64)
	m := &Model{ShortName: "kokoro", Xollama: &xollama.Config{Media: &xollama.Media{
		TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: present, Voices: map[string]string{"narrator": gone}},
	}}}
	_, err := newMediaRunner(m)
	if err == nil || !strings.Contains(err.Error(), gone) || !strings.Contains(err.Error(), "tts") {
		t.Fatalf("err = %v, want a refusal naming the missing voices blob", err)
	}

	m.Xollama.Media.TTS.Voices = nil
	r, err := newMediaRunner(m)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

// A media model pinned to a device is loaded there, and only there: the twin
// the scheduler sees carries the pin, and the pin selects as for any model.
func TestAMediaModelsDevicePinReachesTheScheduler(t *testing.T) {
	m := &Model{Name: "m", ShortName: "m", Digest: "d", Xollama: &xollama.Config{
		Version: 7,
		Media:   &xollama.Media{Image: &xollama.ImageMedia{Model: "sha256:aa"}},
		Devices: &xollama.Devices{Backend: "Vulkan", IDs: []string{"0000:03:00.0"}},
	}}
	twin := mediaTwin(m)
	if twin.Xollama.Devices.IsZero() {
		t.Fatal("the media twin lost the model's device pin")
	}
	gpus := []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{ID: "0", Library: "CUDA"}, PCIID: "0000:01:00.0", FreeMemory: 24 << 30},
		{DeviceID: ml.DeviceID{ID: "0", Library: "Vulkan"}, PCIID: "0000:01:00.0", FreeMemory: 24 << 30},
		{DeviceID: ml.DeviceID{ID: "1", Library: "Vulkan"}, PCIID: "0000:03:00.0", FreeMemory: 16 << 30},
	}
	got, err := selectModelDevices(twin.Xollama, gpus)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PCIID != "0000:03:00.0" || got[0].Library != "Vulkan" {
		t.Fatalf("pinned to the Vulkan card at 03:00.0, the scheduler was offered %+v", got)
	}

	// Unpinned, nothing changes: every GPU is offered and the engine takes
	// the one with the most free memory.
	m.Xollama.Devices = nil
	if got, _ := selectModelDevices(mediaTwin(m).Xollama, gpus); len(got) != len(gpus) {
		t.Fatalf("an unpinned media model was offered %d of %d GPUs", len(got), len(gpus))
	}
}

// exitedLlm is a runner whose process is gone.
type exitedLlm struct{ mockLlm }

func (*exitedLlm) HasExited() bool { return true }

// A media engine that exited is unloaded as its last request ends: it is not
// listed as loaded for the rest of its keep-alive. A text runner is left to
// upstream's own handling.
func TestAMediaEngineThatExitedIsUnloadedAtOnce(t *testing.T) {
	for _, tc := range []struct {
		key  string
		gone bool
	}{
		{mediaKeyPrefix + "abc", true},
		{"/models/blobs/sha256-text", false},
	} {
		s := &Scheduler{
			pendingReqCh:    make(chan *LlmRequest, 1),
			finishedReqCh:   make(chan *LlmRequest, 1),
			expiredCh:       make(chan *runnerRef, 1),
			unloadedCh:      make(chan any, 1),
			loaded:          make(map[string]*runnerRef),
			getGpuFn:        getGpuFn,
			getSystemInfoFn: getSystemInfoFn,
			waitForRecovery: 10 * time.Millisecond,
		}
		m := &Model{ModelPath: tc.key}
		key := schedulerModelKey(m)
		s.loaded[key] = &runnerRef{
			modelKey: key, modelPath: tc.key, refCount: 1, sessionDuration: time.Hour,
			llama: &exitedLlm{mockLlm{modelPath: tc.key}},
		}
		go s.Run(t.Context())
		s.finishedReqCh <- &LlmRequest{model: m}
		gone := false
		for i := 0; i < 100 && !gone; i++ {
			time.Sleep(10 * time.Millisecond)
			s.loadedMu.Lock()
			gone = len(s.loaded) == 0
			s.loadedMu.Unlock()
		}
		if gone != tc.gone {
			t.Errorf("%s: unloaded = %v, want %v", tc.key, gone, tc.gone)
		}
	}
}
