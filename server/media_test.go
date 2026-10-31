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

func capTestScheduler(models ...*Model) *Scheduler {
	s := &Scheduler{loaded: make(map[string]*runnerRef)}
	for _, m := range models {
		r := &runnerRef{modelKey: schedulerModelKey(m), modelPath: m.ModelPath, sessionDuration: time.Hour}
		r.model.Store(m)
		s.loaded[r.modelKey] = r
	}
	return s
}

func mediaEngine(digest string, media xollama.Media) *Model {
	return mediaTwin(&Model{Digest: digest, Xollama: &xollama.Config{Media: &media}})
}

var (
	imageEngine  = xollama.Media{Image: &xollama.ImageMedia{}}
	speechEngine = xollama.Media{TTS: &xollama.TTSMedia{}}
)

func TestAMediaLoadUnloadsOnlyItsOwnKindAtTheCap(t *testing.T) {
	image := mediaEngine("image", imageEngine)
	speech := mediaEngine("speech", speechEngine)
	s := capTestScheduler(&Model{ModelPath: "/models/llm"}, image, speech)

	v := s.kindCapVictim(mediaEngine("image2", imageEngine))
	if v == nil || v.modelKey != schedulerModelKey(image) {
		t.Fatalf("victim = %+v, want the loaded image engine", v)
	}
	if v := s.kindCapVictim(mediaEngine("video", xollama.Media{Video: &xollama.VideoMedia{}})); v != nil {
		t.Errorf("a kind with room unloaded %s", v.modelKey)
	}
	if v := s.kindCapVictim(&Model{ModelPath: "/models/other"}); v != nil {
		t.Errorf("a text load hit a media cap: %s", v.modelKey)
	}

	t.Setenv("XOLLAMA_MAX_LOADED_IMAGE_MODELS", "2")
	if v := s.kindCapVictim(mediaEngine("image2", imageEngine)); v != nil {
		t.Errorf("under a cap of 2 unloaded %s", v.modelKey)
	}
}

func TestAnEngineOfSeveralKindsNeedsRoomUnderEach(t *testing.T) {
	speech := mediaEngine("speech", speechEngine)
	s := capTestScheduler(speech)
	both := xollama.Media{Image: &xollama.ImageMedia{}, TTS: &xollama.TTSMedia{}}
	v := s.kindCapVictim(mediaEngine("both", both))
	if v == nil || v.modelKey != schedulerModelKey(speech) {
		t.Fatalf("victim = %+v, want the speech engine", v)
	}
}

func TestMediaEnginesAreOutsideTheSharedPool(t *testing.T) {
	llm := &Model{ModelPath: "/models/llm"}
	image := mediaEngine("image", imageEngine)
	s := capTestScheduler(llm, image)

	if got := s.sharedRunnerCount(&Model{ModelPath: "/models/other"}); got != 1 {
		t.Errorf("shared count = %d, want 1: the media engine took an LLM's place", got)
	}
	if got := s.sharedRunnerCount(mediaEngine("speech", speechEngine)); got != 0 {
		t.Errorf("a capped media load measured against the shared pool: %d", got)
	}
	if v := s.findSharedRunnerToUnload(); v == nil || v.modelKey != schedulerModelKey(llm) {
		t.Errorf("shared victim = %+v, want the LLM", v)
	}
}

func TestACapOfZeroPutsTheKindBackInTheSharedPool(t *testing.T) {
	t.Setenv("XOLLAMA_MAX_LOADED_IMAGE_MODELS", "0")
	image := mediaEngine("image", imageEngine)
	s := capTestScheduler(&Model{ModelPath: "/models/llm"}, image)

	if v := s.kindCapVictim(mediaEngine("image2", imageEngine)); v != nil {
		t.Errorf("a cap of 0 unloaded %s", v.modelKey)
	}
	if got := s.sharedRunnerCount(&Model{ModelPath: "/models/other"}); got != 2 {
		t.Errorf("shared count = %d, want 2", got)
	}
	if got := s.sharedRunnerCount(mediaEngine("image2", imageEngine)); got != 2 {
		t.Errorf("a kind at 0 measured outside the pool: %d", got)
	}
}

func TestAnLLMWithMediaKeepsItsPlaceInTheSharedPool(t *testing.T) {
	m := &Model{ModelPath: "/models/llm", Xollama: &xollama.Config{Media: &imageEngine}}
	if !inSharedPool(m) {
		t.Error("the LLM of a model with media left the shared pool")
	}
}

// runCapScheduler runs processPending for real over the given loaded models
// and returns the scheduler and the channel the pending load lands on.
func runCapScheduler(t *testing.T, ctx context.Context, pending *Model, models ...*Model) (*Scheduler, chan *Model) {
	t.Helper()
	s := InitScheduler(ctx)
	s.waitForRecovery = 10 * time.Millisecond
	s.getGpuFn = getGpuFn
	s.getSystemInfoFn = getSystemInfoFn
	for _, m := range models {
		r := &runnerRef{
			modelKey: schedulerModelKey(m), modelPath: m.ModelPath, sessionDuration: time.Hour,
			llama: &mockLlm{modelPath: m.ModelPath},
		}
		r.model.Store(m)
		s.loaded[r.modelKey] = r
	}
	landed := make(chan *Model, 1)
	s.loadFn = func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
		landed <- req.model
		return false
	}
	s.pendingReqCh <- &LlmRequest{
		ctx: ctx, model: pending, opts: api.DefaultOptions(),
		successCh: make(chan *runnerRef, 1), errCh: make(chan error, 1),
	}
	s.Run(ctx)
	return s, landed
}

func TestTheSchedulerKeepsAnLLMWhenAnEngineLoadsAtTheLimit(t *testing.T) {
	t.Setenv("OLLAMA_MAX_LOADED_MODELS", "1")
	ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
	defer done()
	llm := &Model{ModelPath: "/models/llm"}
	s, landed := runCapScheduler(t, ctx, mediaEngine("speech", speechEngine), llm)
	select {
	case <-landed:
	case <-ctx.Done():
		t.Fatal("the load never landed")
	}
	s.loadedMu.Lock()
	defer s.loadedMu.Unlock()
	if s.loaded[schedulerModelKey(llm)] == nil {
		t.Error("a speech engine unloaded the LLM")
	}
}

func TestTheSchedulerUnloadsTheSameKindAtItsCap(t *testing.T) {
	ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
	defer done()
	llm := &Model{ModelPath: "/models/llm"}
	image := mediaEngine("image", imageEngine)
	s, landed := runCapScheduler(t, ctx, mediaEngine("image2", imageEngine), llm, image)
	select {
	case <-landed:
	case <-ctx.Done():
		t.Fatal("the load never landed")
	}
	s.loadedMu.Lock()
	defer s.loadedMu.Unlock()
	if s.loaded[schedulerModelKey(image)] != nil {
		t.Error("the image engine at the cap was not unloaded")
	}
	if s.loaded[schedulerModelKey(llm)] == nil {
		t.Error("an image load unloaded the LLM")
	}
}

func cappedDecisionModel(path string) *Model {
	return &Model{ModelPath: path, metadata: ggufMetadata{KV: map[string]any{"general.architecture": "nimble", "nimble.decision.type": "nimble"}}}
}

func TestADecisionModelHasItsOwnCap(t *testing.T) {
	llm := &Model{ModelPath: "/models/llm"}
	nimble := cappedDecisionModel("/models/nimble")
	s := capTestScheduler(llm, nimble, mediaEngine("image", imageEngine))

	v := s.kindCapVictim(cappedDecisionModel("/models/clef"))
	if v == nil || v.modelKey != schedulerModelKey(nimble) {
		t.Fatalf("victim = %+v, want the loaded decision model", v)
	}
	if got := s.sharedRunnerCount(llm); got != 1 {
		t.Errorf("shared count = %d, want 1: a decision model took an LLM's place", got)
	}
	if got := s.sharedRunnerCount(cappedDecisionModel("/models/clef")); got != 0 {
		t.Errorf("a capped decision load measured against the shared pool: %d", got)
	}

	// An MLX decision model has no GGUF metadata.
	mlx := &Model{Config: model.ConfigV2{Capabilities: []string{"decision"}}}
	if !isDecisionModel(mlx) {
		t.Error("an MLX decision model was not recognised")
	}
}

func TestAStockEngineKeepsDecisionModelsInTheSharedPool(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	nimble := cappedDecisionModel("/models/nimble")
	s := capTestScheduler(&Model{ModelPath: "/models/llm"}, nimble)

	if v := s.kindCapVictim(cappedDecisionModel("/models/clef")); v != nil {
		t.Errorf("XOLLAMA_ENGINE=llamacpp unloaded %s for a decision cap", v.modelKey)
	}
	if got := s.sharedRunnerCount(cappedDecisionModel("/models/clef")); got != 2 {
		t.Errorf("shared count = %d, want upstream's 2", got)
	}
}

func TestTheDecisionCapChangesNothingAtZero(t *testing.T) {
	t.Setenv("XOLLAMA_MAX_LOADED_DECISION_MODELS", "0")
	s := capTestScheduler(cappedDecisionModel("/models/nimble"))
	if v := s.kindCapVictim(cappedDecisionModel("/models/clef")); v != nil {
		t.Errorf("a cap of 0 unloaded %s", v.modelKey)
	}
}
