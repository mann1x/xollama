package llm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	got := strings.Join(MediaArgs(m, blob), " ")
	want := "--diffusion-model /blobs/aa --diffusion-vae /blobs/bb --diffusion-llm /blobs/cc " +
		"--diffusion-edit ref --diffusion-device CUDA0 --diffusion-reserve-mib 4096 " +
		"--diffusion-args -W 1024 -H 1024 --steps 4 --cfg-scale 1 --sampling-method euler --vae-tiling"
	if got != want {
		t.Fatalf("args\n got: %s\nwant: %s", got, want)
	}
	// The sd options travel as ONE argument, the way the engine parses them.
	args := MediaArgs(m, blob)
	if last := args[len(args)-1]; !strings.HasPrefix(last, "-W 1024") {
		t.Fatalf("--diffusion-args value = %q, want one string", last)
	}
}

func TestSpeechAndTranscriptionArgs(t *testing.T) {
	outetts := &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b'), Voices: map[string]string{"narrator": d('c'), "host": d('d')}}}
	if got := strings.Join(MediaArgs(outetts, blob), " "); got != "--tts-model /blobs/aa --tts-vocoder /blobs/bb --tts-voice host=/blobs/dd --tts-voice narrator=/blobs/cc" {
		t.Fatalf("outetts: %s (b97 has no --tts-engine; the default must not send one)", got)
	}
	outetts.TTS.Engine, outetts.TTS.Voices = "outetts", nil
	if got := strings.Join(MediaArgs(outetts, blob), " "); strings.Contains(got, "--tts-engine") {
		t.Fatalf("an explicit outetts sent %s; b97 refuses a flag it does not know", got)
	}
	kokoro := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: d('a')}}
	if got := strings.Join(MediaArgs(kokoro, blob), " "); got != "--tts-engine audiocpp --tts-model /blobs/aa" {
		t.Fatalf("audiocpp: %s", got)
	}
	stt := &xollama.Media{STT: &xollama.STTMedia{Model: d('a'), Threads: 4, MediaEngine: xollama.MediaEngine{Device: "CPU"}}}
	if got := strings.Join(MediaArgs(stt, blob), " "); got != "--stt-model /blobs/aa --stt-device CPU --stt-threads 4" {
		t.Fatalf("stt: %s", got)
	}
}

func TestVideoArgs(t *testing.T) {
	m := &xollama.Media{Video: &xollama.VideoMedia{
		Model: d('a'), VAE: d('b'), TextEncoder: d('c'),
		Defaults: &xollama.VideoDefaults{Width: 832, Height: 480, Frames: 33, FPS: 16, CFG: fp(6), FlowShift: fp(3)},
	}}
	got := strings.Join(MediaArgs(m, blob), " ")
	want := "--video-model /blobs/aa --video-vae /blobs/bb --video-t5xxl /blobs/cc " +
		"--video-args -W 832 -H 480 --cfg-scale 6 --flow-shift 3 --video-frames 33 --fps 16"
	if got != want {
		t.Fatalf("args\n got: %s\nwant: %s", got, want)
	}
}

func TestAudioCppSpeechNeedsAnEngineThatReadsBlobsAsTheyAre(t *testing.T) {
	kokoro := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: d('a')}}
	if got := strings.Join(MediaFeatures(kokoro), ","); got != "audio_speech_v1,audio_speech_content_format_v1,audio_speech_audiocpp_v1" {
		t.Fatalf("audiocpp features = %s; an engine that judges a file by its name, or has no audio.cpp sidecar loaded, must be refused", got)
	}
	voiced := &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b'), Voices: map[string]string{"narrator": d('c')}}}
	if got := strings.Join(MediaFeatures(voiced), ","); got != "audio_speech_v1,audio_speech_voice_files_v1" {
		t.Fatalf("voices features = %s; each voice goes to the engine as --tts-voice NAME=<blob>", got)
	}
}

func TestAnUnreadableHealthIsNotReadAsMissingFeatures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not the engine</html>"))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	r := &mediaRunner{port: p, done: make(chan struct{}), client: srv.Client(), need: []string{FeatureSpeech}}
	err := r.WaitUntilRunning(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unreadable body") || strings.Contains(err.Error(), "does not offer") {
		t.Fatalf("err = %v, want the unreadable /health named, not a missing feature", err)
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

func TestATemplateOnCPUIsNeverPlacedOnAGPU(t *testing.T) {
	cpu := xollama.MediaEngine{Device: "CPU"}
	m := &xollama.Media{
		Image: &xollama.ImageMedia{Model: "i", MediaEngine: cpu},
		STT:   &xollama.STTMedia{Model: "s", MediaEngine: xollama.MediaEngine{Device: "cpu"}},
	}
	r, err := NewMediaRunner("media:x", m, func(d string) string { return d }, func(string) int64 { return 1 })
	if err != nil {
		t.Fatal(err)
	}
	if !r.(*mediaRunner).cpu {
		t.Fatal("a template with every engine on CPU would be placed on a GPU")
	}
	// One engine left to the scheduler is enough to use a GPU.
	m.STT.Device = ""
	if mediaOnCPU(m) {
		t.Fatal("a template with one engine unpinned was kept on CPU")
	}
}

func TestAudioCppSpeechIsNeverPlacedOnAGPU(t *testing.T) {
	kokoro := &xollama.Media{TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: d('a')}}
	if !mediaOnCPU(kokoro) {
		t.Fatal("an audio.cpp speech model, which has no GPU backend, would be placed on a GPU")
	}
	outetts := &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b')}}
	if mediaOnCPU(outetts) {
		t.Fatal("OuteTTS with no device stated was kept off the GPU")
	}
	mixed := &xollama.Media{TTS: kokoro.TTS, Image: &xollama.ImageMedia{Model: d('c')}}
	if mediaOnCPU(mixed) {
		t.Fatal("an image engine beside audio.cpp was kept off the GPU")
	}
}

func TestAFailedLoadReportsTheCauseNotOnlyTheLastLine(t *testing.T) {
	var w tailWriter
	stderr := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	defer func() { os.Stderr = stderr }()
	w.Write([]byte("booting\nfatal error: --gpu vulkan was explicitly requested but Vulkan is not usable on this system\n" +
		"  - no Vulkan-capable device was detected\n\n  - retry with --gpu auto\n"))
	got := w.last()
	if !strings.Contains(got, "fatal error: --gpu vulkan") || !strings.HasSuffix(got, "retry with --gpu auto") {
		t.Fatalf("tail = %q, want the cause and the advice", got)
	}
	for range 10 {
		w.Write([]byte("noise\n"))
	}
	if n := strings.Count(w.last(), "noise"); n != tailLines {
		t.Fatalf("tail kept %d lines, want %d", n, tailLines)
	}
	if (&tailWriter{}).last() != "no output" {
		t.Fatal("an engine that said nothing is not reported as such")
	}
}

func TestATemplateWhoseDefaultFormatNeedsTheCodecAsksForIt(t *testing.T) {
	tts := func(format string) *xollama.Media {
		return &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b'), Defaults: &xollama.TTSDefaults{ResponseFormat: format}}}
	}
	video := func(format string) *xollama.Media {
		return &xollama.Media{Video: &xollama.VideoMedia{Model: d('a'), Defaults: &xollama.VideoDefaults{OutputFormat: format}}}
	}
	cases := []struct {
		name  string
		m     *xollama.Media
		codec bool
	}{
		// What the engine writes on its own (opencoti #691).
		{"speech, no format stated", tts(""), false},
		{"speech wav", tts("wav"), false},
		{"speech pcm", tts("pcm"), false},
		{"video, no format stated", video(""), false},
		{"video avi", video("avi"), false},
		{"video webm", video("webm"), false},
		// What only the codec sidecar writes.
		{"speech mp3", tts("mp3"), true},
		{"speech opus", tts("opus"), true},
		{"speech aac", tts("aac"), true},
		{"speech flac", tts("flac"), true},
		{"video mp4", video("mp4"), true},
		{"video webp", video("webp"), true},
		// No defaults at all, and kinds that write neither.
		{"speech without defaults", &xollama.Media{TTS: &xollama.TTSMedia{Model: d('a'), Vocoder: d('b')}}, false},
		{"transcription", &xollama.Media{STT: &xollama.STTMedia{Model: d('a'), Defaults: &xollama.STTDefaults{ResponseFormat: "json"}}}, false},
		{"image", &xollama.Media{Image: &xollama.ImageMedia{Model: d('a'), Defaults: &xollama.ImageDefaults{OutputFormat: "png"}}}, false},
	}
	for _, c := range cases {
		if got := slices.Contains(MediaFeatures(c.m), FeatureMediaCodec); got != c.codec {
			t.Errorf("%s: asks for the codec = %v, want %v", c.name, got, c.codec)
		}
	}
}

func TestAMissingSidecarIsNamed(t *testing.T) {
	r := fakeEngine(t, []string{FeatureSpeech, FeatureSpeechContentFormat})
	r.need = []string{FeatureSpeech, FeatureSpeechContentFormat, FeatureSpeechAudioCpp, FeatureMediaCodec}
	err := r.WaitUntilRunning(context.Background())
	if err == nil {
		t.Fatal("an engine with neither sidecar loaded was accepted")
	}
	for _, want := range []string{FeatureSpeechAudioCpp, "oc-audiocpp", FeatureMediaCodec, "oc-codec"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), FeatureSpeechContentFormat) {
		t.Errorf("err = %v; a feature the engine has is not missing", err)
	}
}

func TestAnEngineWithItsSidecarsLoadedServesAnAudioCppTemplate(t *testing.T) {
	// The /health list of an audio.cpp boot on b117 with both sidecars.
	r := fakeEngine(t, []string{FeatureSpeech, FeatureSpeechContentFormat, FeatureSpeechAudioCpp, FeatureMediaCodec})
	r.need = MediaFeatures(&xollama.Media{TTS: &xollama.TTSMedia{
		Engine: "audiocpp", Model: d('a'), Defaults: &xollama.TTSDefaults{ResponseFormat: "mp3"},
	}})
	if len(r.need) != 4 {
		t.Fatalf("need = %v", r.need)
	}
	if err := r.WaitUntilRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
}
