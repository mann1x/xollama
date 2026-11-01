package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func sfxTemplate(t *testing.T, fixed ...string) *xollama.Media {
	seconds := 2.5
	return &xollama.Media{SFX: &xollama.SFXMedia{
		Model:       mediaDigest(t, []byte("stable-audio")),
		MediaEngine: xollama.MediaEngine{Fixed: fixed},
		Defaults:    &xollama.SFXDefaults{Seconds: &seconds, Steps: 6, ResponseFormat: "wav"},
	}}
}

func TestASoundEffectIsFilledFromTheTemplateAndKeepsItsSeed(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"sfx": sfxTemplate(t, "steps")})

	w := postJSON(t, s.SoundEffectsHandler, `{"model":"sfx","prompt":"a click","steps":50,"response_format":"pcm"}`)
	if w.Code != http.StatusOK || w.Body.String() != "RIFFclip" {
		t.Fatalf("clip = %d %q", w.Code, w.Body.String())
	}
	// Without the seed a client cannot repeat a clip it liked.
	for h, want := range map[string]string{"X-OC-Seed": "42", "X-OC-Seconds": "2.5", "X-OC-Sample-Rate": "44100", "X-OC-Channels": "2", "Content-Type": "audio/wav"} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	var got map[string]any
	_ = json.Unmarshal(e.last(t).body, &got)
	if e.last(t).path != "/v1/audio/generations" {
		t.Fatalf("reached the engine as %s", e.last(t).path)
	}
	if got["seconds"] != 2.5 || got["steps"] != 6.0 || got["response_format"] != "pcm" {
		t.Fatalf("engine got %v", got)
	}
}

func TestAModelWithoutSoundEffectsIsRefusedByName(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{"kokoro": {TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: mediaDigest(t, []byte("k"))}}})
	w := postJSON(t, s.SoundEffectsHandler, `{"model":"kokoro","prompt":"a click"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "sound effects") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestAPinWithoutSoundEffectsIsRefusedByName(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{"sfx": sfxTemplate(t)})
	restore := hasSoundEffects
	hasSoundEffects = func() bool { return false }
	t.Cleanup(func() { hasSoundEffects = restore })

	w := postJSON(t, s.SoundEffectsHandler, `{"model":"sfx","prompt":"a click"}`)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "audio_sfx_audiocpp_v1") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestSoundEffectsAreACapabilityWithTheirOwnCap(t *testing.T) {
	m := &Model{Xollama: &xollama.Config{Media: sfxTemplate(t)}}
	if !slices.Contains(mediaCapabilities(m), CapabilitySoundEffects) {
		t.Fatalf("capabilities = %v", mediaCapabilities(m))
	}
	sfx := mediaEngine("sfx", *sfxTemplate(t))
	s := capTestScheduler(sfx)
	if v := s.kindCapVictim(mediaEngine("sfx2", *sfxTemplate(t))); v == nil {
		t.Fatal("a second sound-effects engine did not unload the first at the cap")
	}
	if v := s.kindCapVictim(mediaEngine("speech", speechEngine)); v != nil {
		t.Fatalf("a speech load unloaded %s", v.modelKey)
	}
}
