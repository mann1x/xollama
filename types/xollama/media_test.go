package xollama

import (
	"encoding/json"
	"strings"
	"testing"
)

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func ptr[T any](v T) *T { return &v }

// kitchen is a model carrying every kind of media, as a media-kit template
// would.
func kitchen() *Config {
	return &Config{Media: &Media{
		Image: &ImageMedia{
			Model: digest('a'), VAE: digest('b'), LLM: digest('c'), Edit: "reference",
			MediaEngine: MediaEngine{Fixed: []string{"width"}},
			Defaults:    &ImageDefaults{Width: 1024, Height: 1024, Steps: 4, CFG: ptr(1.0), OutputFormat: "png"},
		},
		STT: &STTMedia{Model: digest('d'), Defaults: &STTDefaults{ResponseFormat: "json"}},
		TTS: &TTSMedia{
			Engine: "audiocpp", Model: digest('e'),
			VoiceMap: map[string]string{"alloy": "af_heart"},
			Defaults: &TTSDefaults{Voice: "af_heart", ResponseFormat: "mp3", Speed: ptr(1.0)},
		},
	}}
}

func TestAModelWithMediaRoundTripsAtSchemaSeven(t *testing.T) {
	data, err := kitchen().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 7 {
		t.Fatalf("version = %d, want 7: an older build must refuse media, not drop it", got.Version)
	}
	if got.Media.Image.Defaults.Width != 1024 || got.Media.TTS.VoiceMap["alloy"] != "af_heart" ||
		got.Media.Image.Fixed[0] != "width" || *got.Media.TTS.Defaults.Speed != 1.0 {
		t.Fatalf("media did not survive the round trip: %s", data)
	}
}

func TestMediaRaisesNoFloorWhenAbsent(t *testing.T) {
	data, err := (&Config{FlashAttention: "on"}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Version int }
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != SchemaVersionBase {
		t.Fatalf("version = %d, want %d", v.Version, SchemaVersionBase)
	}
}

func TestAMediaOnlyConfigIsNotEmpty(t *testing.T) {
	if kitchen().IsZero() {
		t.Fatal("a config carrying only media reads as empty, so create would drop its layer")
	}
	if !(&Config{Media: &Media{}}).IsZero() {
		t.Fatal("an empty media block should store nothing")
	}
}

func TestComponentsAreNamedByKindAndRoleInAStableOrder(t *testing.T) {
	got := kitchen().Media.Components()
	want := []MediaComponent{
		{"media/image.model", digest('a')},
		{"media/image.vae", digest('b')},
		{"media/image.llm", digest('c')},
		{"media/stt.model", digest('d')},
		{"media/tts.model", digest('e')},
	}
	if len(got) != len(want) {
		t.Fatalf("components = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("component %d = %v, want %v", i, got[i], want[i])
		}
	}
	if k := kitchen().Media.Kinds(); strings.Join(k, ",") != "image,stt,tts" {
		t.Fatalf("kinds = %v", k)
	}
}

func TestOuteTTSNeedsItsVocoderAndAudioCppDoesNot(t *testing.T) {
	outetts := &Config{Version: 7, Media: &Media{TTS: &TTSMedia{Model: digest('a')}}}
	if err := outetts.Validate(); err == nil || !strings.Contains(err.Error(), "media.tts.vocoder") {
		t.Fatalf("outetts without a vocoder: err = %v", err)
	}
	outetts.Media.TTS.Vocoder = digest('b')
	if err := outetts.Validate(); err != nil {
		t.Fatal(err)
	}
	audiocpp := &Config{Version: 7, Media: &Media{TTS: &TTSMedia{Engine: "audiocpp", Model: digest('a')}}}
	if err := audiocpp.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*Config)
	}{
		{"stock engine", "needs the opencoti engine", func(c *Config) { c.Engine = EngineLlamaCpp }},
		{"bad digest", "media.image.vae", func(c *Config) { c.Media.Image.VAE = "flux2-vae.safetensors" }},
		{"no image model", "media.image.model is required", func(c *Config) { c.Media.Image.Model = "" }},
		{"no stt model", "media.stt.model is required", func(c *Config) { c.Media.STT.Model = "" }},
		{"edit mode", "media.image.edit", func(c *Config) { c.Media.Image.Edit = "inpaint" }},
		{"image format", "media.image.defaults.output_format", func(c *Config) { c.Media.Image.Defaults.OutputFormat = "webp" }},
		{"steps", "steps", func(c *Config) { c.Media.Image.Defaults.Steps = -1 }},
		{"width", "width", func(c *Config) { c.Media.Image.Defaults.Width = 100000 }},
		{"cfg", "cfg", func(c *Config) { c.Media.Image.Defaults.CFG = ptr(-1.0) }},
		{"strength", "strength", func(c *Config) { c.Media.Image.Defaults.Strength = ptr(0.0) }},
		{"reserve", "reserve_mib", func(c *Config) { c.Media.Image.ReserveMiB = -1 }},
		{"empty arg", "empty argument", func(c *Config) { c.Media.Image.Args = []string{" "} }},
		{"fixed unknown", "unknown field", func(c *Config) { c.Media.Image.Fixed = []string{"colour"} }},
		{"fixed unstated", "no value", func(c *Config) { c.Media.Image.Fixed = []string{"seed"} }},
		{"fixed twice", "twice", func(c *Config) { c.Media.Image.Fixed = []string{"width", "width"} }},
		{"stt format", "media.stt.defaults.response_format", func(c *Config) { c.Media.STT.Defaults.ResponseFormat = "xml" }},
		{"stt task", "media.stt.defaults.task", func(c *Config) { c.Media.STT.Defaults.Task = "summarise" }},
		{"stt threads", "threads", func(c *Config) { c.Media.STT.Threads = -2 }},
		{"tts engine", "media.tts.engine", func(c *Config) { c.Media.TTS.Engine = "piper" }},
		{"tts format", "media.tts.defaults.response_format", func(c *Config) { c.Media.TTS.Defaults.ResponseFormat = "ogg" }},
		{"tts speed", "speed", func(c *Config) { c.Media.TTS.Defaults.Speed = ptr(5.0) }},
		{"voice map", "voice_map", func(c *Config) { c.Media.TTS.VoiceMap["nova"] = "" }},
		{"video model", "media.video.model is required", func(c *Config) { c.Media.Video = &VideoMedia{} }},
		{"video frames", "frames", func(c *Config) {
			c.Media.Video = &VideoMedia{Model: digest('f'), Defaults: &VideoDefaults{Frames: 5000}}
		}},
		{"video format", "media.video.defaults.output_format", func(c *Config) {
			c.Media.Video = &VideoMedia{Model: digest('f'), Defaults: &VideoDefaults{OutputFormat: "mov"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := kitchen()
			c.Version = 7
			if err := c.Validate(); err != nil {
				t.Fatalf("the base config is invalid: %v", err)
			}
			tc.edit(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

func TestAVideoTemplateIsAcceptedBeforeTheEngineHasVideo(t *testing.T) {
	c := &Config{Media: &Media{Video: &VideoMedia{
		Model: digest('a'), VAE: digest('b'), TextEncoder: digest('c'),
		Defaults: &VideoDefaults{Width: 832, Height: 480, Frames: 33, FPS: 16, CFG: ptr(6.0), Sampler: "euler", FlowShift: ptr(3.0)},
	}}}
	if _, err := c.Marshal(); err != nil {
		t.Fatal(err)
	}
}

func TestACloneSharesNothingWithItsOriginal(t *testing.T) {
	orig := kitchen().Media
	c := orig.Clone()
	c.Image.Defaults.Width = 512
	c.Image.Fixed[0] = "height"
	c.TTS.VoiceMap["alloy"] = "bf_emma"
	c.STT.Defaults.Language = "it"
	if orig.Image.Defaults.Width != 1024 || orig.Image.Fixed[0] != "width" ||
		orig.TTS.VoiceMap["alloy"] != "af_heart" || orig.STT.Defaults.Language != "" {
		t.Fatalf("editing the clone changed the original: %+v", orig)
	}
}

func TestMediaPruneDropsWhatStatesNothing(t *testing.T) {
	m := &Media{Image: &ImageMedia{Defaults: &ImageDefaults{}}, TTS: &TTSMedia{VoiceMap: map[string]string{}}}
	if got := m.Prune(); got != nil {
		t.Fatalf("Prune = %+v, want nil", got)
	}
	m = &Media{STT: &STTMedia{Model: digest('a'), Defaults: &STTDefaults{}}}
	got := m.Prune()
	if got == nil || got.STT == nil || got.STT.Defaults != nil {
		t.Fatalf("Prune = %+v, want the model kept and the empty defaults dropped", got)
	}
}

func TestMapComponentsReachesEveryComponent(t *testing.T) {
	m := kitchen().Media
	m.Video = &VideoMedia{Model: digest('f'), VAE: digest('g'), TextEncoder: digest('h')}
	m.TTS.Vocoder, m.TTS.Voices, m.Image.LLMVision = digest('i'), digest('j'), digest('k')
	n := 0
	m.MapComponents(func(s string) string { n++; return "x" + s })
	if n != len(m.Components()) || n != 11 {
		t.Fatalf("mapped %d of %d components", n, len(m.Components()))
	}
	for _, c := range m.Components() {
		if !strings.HasPrefix(c.Digest, "x") {
			t.Fatalf("%s not mapped", c.Name)
		}
	}
}
