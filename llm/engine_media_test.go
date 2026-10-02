package llm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

func d(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func fp(f float64) *float64 { return &f }

func blob(digest string) string { return "/blobs/" + digest[7:9] }

func TestAKleinTemplateBootsWithItsOwnDefaults(t *testing.T) {
	m := &xollama.Media{Image: &xollama.ImageMedia{
		Model: d('a'), VAE: d('b'), LLM: d('c'), Edit: "reference",
		MediaEngine: xollama.MediaEngine{Device: "CUDA0", ReserveMiB: 4096, Args: []string{"--vae-tiling"}},
		Defaults:    &xollama.ImageDefaults{Width: 1024, Height: 1024, Steps: 4, CFG: fp(1), Sampler: "euler"},
	}}
	got := strings.Join(MediaArgs(m, blob, ""), " ")
	want := "--diffusion-model /blobs/aa --diffusion-vae /blobs/bb --diffusion-llm /blobs/cc " +
		"--diffusion-edit ref --diffusion-device CUDA0 --diffusion-reserve-mib 4096 " +
		"--diffusion-args -W 1024 -H 1024 --steps 4 --cfg-scale 1 --sampling-method euler --vae-tiling"
	if got != want {
		t.Fatalf("args\n got: %s\nwant: %s", got, want)
	}
	// The sd options travel as ONE argument, the way the engine parses them.
	args := MediaArgs(m, blob, "")
	if last := args[len(args)-1]; !strings.HasPrefix(last, "-W 1024") {
		t.Fatalf("--diffusion-args value = %q, want one string", last)
	}
}

func TestSpeechAndTranscriptionArgs(t *testing.T) {
	outetts := &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b')}}
	if got := strings.Join(MediaArgs(outetts, blob, "/voices"), " "); got != "--tts-model /blobs/aa --tts-vocoder /blobs/bb --tts-voices /voices" {
		t.Fatalf("outetts: %s (b97 has no --tts-engine; the default must not send one)", got)
	}
	outetts.TTS.Engine = "outetts"
	if got := strings.Join(MediaArgs(outetts, blob, ""), " "); strings.Contains(got, "--tts-engine") {
		t.Fatalf("an explicit outetts sent %s; b97 refuses a flag it does not know", got)
	}
	kokoro := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: d('a')}}
	if got := strings.Join(MediaArgs(kokoro, blob, ""), " "); got != "--tts-engine audiocpp --tts-model /blobs/aa" {
		t.Fatalf("audiocpp: %s", got)
	}
	stt := &xollama.Media{STT: &xollama.STTMedia{Model: d('a'), Threads: 4, MediaEngine: xollama.MediaEngine{Device: "CPU"}}}
	if got := strings.Join(MediaArgs(stt, blob, ""), " "); got != "--stt-model /blobs/aa --stt-device CPU --stt-threads 4" {
		t.Fatalf("stt: %s", got)
	}
}

func TestVideoArgs(t *testing.T) {
	m := &xollama.Media{Video: &xollama.VideoMedia{
		Model: d('a'), VAE: d('b'), TextEncoder: d('c'),
		Defaults: &xollama.VideoDefaults{Width: 832, Height: 480, Frames: 33, FPS: 16, CFG: fp(6), FlowShift: fp(3)},
	}}
	got := strings.Join(MediaArgs(m, blob, ""), " ")
	want := "--video-model /blobs/aa --video-vae /blobs/bb --video-t5xxl /blobs/cc " +
		"--video-args -W 832 -H 480 --cfg-scale 6 --flow-shift 3 --video-frames 33 --fps 16"
	if got != want {
		t.Fatalf("args\n got: %s\nwant: %s", got, want)
	}
}

func TestMediaFeaturesAndEstimate(t *testing.T) {
	m := &xollama.Media{
		Image: &xollama.ImageMedia{Model: d('a'), Edit: "none"},
		TTS:   &xollama.TTSMedia{Model: d('b'), MediaEngine: xollama.MediaEngine{ReserveMiB: 100}},
	}
	if got := strings.Join(MediaFeatures(m), ","); got != "images_generate_v1,audio_speech_v1" {
		t.Fatalf("features = %s; an image model that refuses edits needs no edit route", got)
	}
	size := func(string) int64 { return 1 << 30 }
	want := uint64(2<<30) + uint64(defaultImageReserveMiB)<<20 + 100<<20
	if got := MediaEstimate(m, size); got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestTheMediaGoesToTheGPUWithTheMostRoom(t *testing.T) {
	gpus := []ml.DeviceInfo{
		{DeviceID: ml.DeviceID{ID: "0"}, FreeMemory: 2 << 30},
		{DeviceID: ml.DeviceID{ID: "1"}, FreeMemory: 20 << 30},
	}
	g, ok, fits := pickMediaGPU(gpus, 8<<30)
	if !ok || !fits || g.ID != "1" {
		t.Fatalf("picked %v ok=%v fits=%v", g.ID, ok, fits)
	}
	if _, _, fits := pickMediaGPU(gpus, 40<<30); fits {
		t.Fatal("40 GiB fits in 20")
	}
	r := &mediaRunner{estimate: 40 << 30, done: make(chan struct{})}
	if _, err := r.Load(context.Background(), ml.SystemInfo{}, gpus, true); err != ErrLoadRequiredFull {
		t.Fatalf("err = %v, want ErrLoadRequiredFull so the scheduler evicts and retries", err)
	}
}

func fakeEngine(t *testing.T, features []string) *mediaRunner {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "features": features})
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return &mediaRunner{port: p, done: make(chan struct{}), client: srv.Client()}
}

func TestAnEngineWithoutTheNeededFeatureIsRefused(t *testing.T) {
	r := fakeEngine(t, []string{FeatureImagesGenerate})
	r.need = []string{FeatureImagesGenerate, FeatureSpeech}
	err := r.WaitUntilRunning(context.Background())
	if err == nil || !strings.Contains(err.Error(), FeatureSpeech) {
		t.Fatalf("err = %v, want a refusal naming %s", err, FeatureSpeech)
	}

	r = fakeEngine(t, []string{FeatureImagesGenerate, FeatureSpeech})
	r.need = []string{FeatureSpeech}
	if err := r.WaitUntilRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.Features(); len(got) != 2 {
		t.Fatalf("features = %v", got)
	}
	if err := r.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAMediaRunnerServesNoText(t *testing.T) {
	r := &mediaRunner{done: make(chan struct{})}
	if err := r.Completion(context.Background(), CompletionRequest{}, nil); err != errMediaOnly {
		t.Fatalf("completion err = %v", err)
	}
	if _, _, err := r.Embedding(context.Background(), "x"); err != errMediaOnly {
		t.Fatalf("embedding err = %v", err)
	}
}
