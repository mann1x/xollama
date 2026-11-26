package server

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// The request's keep_alive wins, then the model's own, then the server's.
func TestTheModelsKeepAliveIsUsedWhenTheRequestNamesNone(t *testing.T) {
	def := 5 * time.Minute
	stated := &Model{Xollama: &xollama.Config{Version: 1, KeepAlive: "1h"}}
	forever := &Model{Xollama: &xollama.Config{Version: 1, KeepAlive: xollama.KeepAliveForever}}
	for _, tc := range []struct {
		name string
		m    *Model
		req  *api.Duration
		want time.Duration
	}{
		{"the request wins", stated, &api.Duration{Duration: 0}, 0},
		{"the model's own", stated, nil, time.Hour},
		{"forever", forever, nil, time.Duration(math.MaxInt64)},
		{"a model with no config keeps the server's", &Model{}, nil, def},
		{"a model stating nothing keeps the server's", &Model{Xollama: &xollama.Config{Version: 1}}, nil, def},
		{"no model", nil, nil, def},
	} {
		if got := modelKeepAlive(tc.m, tc.req, def); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A media model whose config says forever is loaded for ever by a request
// that names no keep_alive (the media routes never do), and still unloads by
// its name: xollama stop sends keep_alive 0, which expires it.
func TestAMediaModelKeptForeverStillUnloadsByName(t *testing.T) {
	t.Setenv("OLLAMA_KEEP_ALIVE", "")
	for _, tc := range []struct {
		name      string
		keepAlive string
		want      time.Duration
	}{
		{"forever", xollama.KeepAliveForever, time.Duration(math.MaxInt64)},
		{"unset keeps the server's default", "", 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
			defer done()
			s := InitScheduler(ctx)
			s.newServerFn = func(ml.SystemInfo, []ml.DeviceInfo, string, *gguf.Model, []string, []string, api.Options, int, llm.LlamaServerConfig) (llm.LlamaServer, error) {
				t.Fatal("a media twin reached the LLM constructor")
				return nil, nil
			}
			fake := &fakeMedia{mockLlm{vramSize: 512 << 20, vramByGPU: map[ml.DeviceID]uint64{}}}
			old := newMediaRunnerFn
			newMediaRunnerFn = func(m *Model) (llm.LlamaServer, error) {
				fake.modelPath = m.ModelPath
				return fake, nil
			}
			t.Cleanup(func() { newMediaRunnerFn = old })

			twin := mediaTwin(&Model{
				Name: "kitten", ShortName: "kitten", Digest: "sha256:k1",
				Xollama: &xollama.Config{Version: 7, KeepAlive: tc.keepAlive, Media: &xollama.Media{TTS: &xollama.TTSMedia{Model: "z"}}},
			})
			req := &LlmRequest{
				ctx: ctx, model: twin, opts: api.DefaultOptions(),
				successCh: make(chan *runnerRef, 1), errCh: make(chan error, 1),
			}
			s.load(req, ml.SystemInfo{}, nil, false)
			var r *runnerRef
			select {
			case err := <-req.errCh:
				t.Fatal(err)
			case r = <-req.successCh:
			case <-ctx.Done():
				t.Fatal("no runner")
			}
			r.refMu.Lock()
			got := r.sessionDuration
			r.refMu.Unlock()
			if got != tc.want {
				t.Fatalf("the runner stays loaded %v after its last request, want %v", got, tc.want)
			}

			// keep_alive 0 by name (what xollama stop sends) reaches the twin.
			s.expireRunner(&Model{Name: "kitten"})
			r.refMu.Lock()
			defer r.refMu.Unlock()
			if r.sessionDuration != 0 || r.expiresAt.After(time.Now()) {
				t.Fatalf("unloading by name left the runner with %v to live", r.sessionDuration)
			}
		})
	}
}

func TestPreloadNamesAreTakenOnceInOrder(t *testing.T) {
	got := preloadNames(" mannix/flux2-klein:4b, ,mannix/whisper:large-v3-turbo,mannix/flux2-klein:4b ,")
	want := []string{"mannix/flux2-klein:4b", "mannix/whisper:large-v3-turbo"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%q, want %q", got, want)
	}
	if preloadNames("") != nil {
		t.Fatal("an empty list names a model")
	}
}

// A media-only model is loaded through its media engine, a cloud model not
// at all.
func TestPreloadLoadsAMediaModelThroughItsEngine(t *testing.T) {
	media := &Model{Name: "kitten", Xollama: &xollama.Config{Version: 7, Media: &xollama.Media{TTS: &xollama.TTSMedia{Model: "z"}}}}
	text, kind, skip := preloadPlan(media)
	if text || kind != CapabilitySpeech || skip != "" {
		t.Fatalf("media-only model: text %v kind %q skip %q", text, kind, skip)
	}
	cloud := &Model{Name: "c"}
	cloud.Config.RemoteHost, cloud.Config.RemoteModel = "https://ollama.com", "gpt-oss:120b"
	if _, _, skip := preloadPlan(cloud); skip == "" {
		t.Fatal("a cloud model was planned for a local load")
	}
	if _, _, skip := preloadPlan(&Model{Name: "empty"}); skip == "" {
		t.Fatal("a model with nothing to serve was planned for a load")
	}
}
