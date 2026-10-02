package mediahub

import (
	"slices"

	"github.com/ollama/ollama/types/xollama"
)

// Entry is one media template the catalog offers: what it is, and the
// xollama.json media block it becomes, with hf.co references where the
// digests will go.
//
// The models and their settings are the ones SurfSense's desktop app ships
// and reviewed (surfsense_local/backend/scripts/local_manifest/entries.py at
// 666bbb07), plus the OuteTTS pair opencoti's b97 engine was tested with.
type Entry struct {
	ID          string
	Name        string
	Kind        string // image, stt, tts, video, or mix
	Family      string
	License     string
	Description string
	// Needs says what the engine must have for it, when b97 does not.
	Needs string
	// Template is the name the entry is published under on ollama.com, and
	// Published says it is there: xollama pull Template gets the same model
	// media create builds. The names are mannix/<family>:<size> (owner,
	// 2026-10-02); stock ollama pulls them but cannot run a media-only one.
	Template  string
	Published bool
	Media     xollama.Media
}

// Refs lists the entry's component references, in component order.
func (e Entry) Refs() []string {
	var out []string
	for _, c := range e.Media.Components() {
		if !slices.Contains(out, c.Digest) {
			out = append(out, c.Digest)
		}
	}
	return out
}

func f(v float64) *float64 { return &v }

const (
	qwen3_4B    = "hf.co/unsloth/Qwen3-4B-GGUF/Qwen3-4B-Q4_0.gguf"
	umt5        = "hf.co/city96/umt5-xxl-encoder-gguf/umt5-xxl-encoder-Q4_K_M.gguf"
	audioCppHub = "hf.co/audio-cpp/audio.cpp-gguf/"
	needsM7     = "opencoti M7"
)

// openAIVoices maps OpenAI's six voice names to each voice model's own.
var (
	kokoroVoices     = map[string]string{"alloy": "af_alloy", "echo": "am_echo", "fable": "bm_fable", "onyx": "am_onyx", "nova": "af_nova", "shimmer": "af_heart"}
	supertonicVoices = map[string]string{"alloy": "F1", "echo": "M1", "fable": "M2", "onyx": "M3", "nova": "F2", "shimmer": "F3"}
	kittenVoices     = map[string]string{"alloy": "Bella", "echo": "Jasper", "fable": "Hugo", "onyx": "Bruno", "nova": "Luna", "shimmer": "Rosie"}
)

func klein() *xollama.ImageMedia {
	return &xollama.ImageMedia{
		Model: "hf.co/leejet/FLUX.2-klein-4B-GGUF/flux-2-klein-4b-Q8_0.gguf",
		VAE:   "hf.co/Comfy-Org/vae-text-encorder-for-flux-klein-4b/split_files/vae/flux2-vae.safetensors",
		LLM:   qwen3_4B,
		// No edit mode: the engine decides by family. M7's auto edits FLUX.2
		// from reference images, and b97, which has no switch, edits as
		// img2img; stating one would keep the template off b97.
		Defaults: &xollama.ImageDefaults{
			Width: 1024, Height: 1024, Steps: 4, CFG: f(1), Sampler: "euler", OutputFormat: "png",
		},
	}
}

func whisper() *xollama.STTMedia {
	return &xollama.STTMedia{
		Model:    "hf.co/ggerganov/whisper.cpp/ggml-large-v3-turbo-q8_0.bin",
		Defaults: &xollama.STTDefaults{ResponseFormat: "json"},
	}
}

func kokoro() *xollama.TTSMedia {
	return &xollama.TTSMedia{
		Engine: "audiocpp", Model: audioCppHub + "Kokoro-82M-GGUF/kokoro-82m-q8_0.gguf",
		VoiceMap: kokoroVoices,
		Defaults: &xollama.TTSDefaults{Voice: "af_heart", ResponseFormat: "mp3"},
	}
}

func wanDefaults(width, height, fps int) *xollama.VideoDefaults {
	return &xollama.VideoDefaults{
		Width: width, Height: height, Frames: 33, FPS: fps, CFG: f(6), Sampler: "euler", FlowShift: f(3), OutputFormat: "mp4",
	}
}

// wan21Reserve is Wan2.1 1.3B's working memory at its template clip,
// 640x352x33: measured on an RTX 3090 with opencoti b105 (2026-10-02), the
// peak 17,044 MiB less 5.7 GiB after boot, about 11.3 GiB, rounded up. At
// 832x480 the VAE decode alone wants ~19 GB, more than a 24 GB card has
// beside the weights, so the template's clip is the smaller one.
const wan21Reserve = 12288

var catalog = []Entry{
	{
		ID: "flux2-klein-4b", Template: "mannix/flux2-klein:4b", Published: true, Name: "FLUX.2 klein 4B", Kind: "image", Family: "FLUX.2", License: "apache-2.0",
		Description: "Generates and edits (reference images) in four steps, in the least memory.",
		Media:       xollama.Media{Image: klein()},
	},
	{
		ID: "z-image-turbo", Template: "mannix/z-image:turbo", Published: true, Name: "Z-Image Turbo", Kind: "image", Family: "Z-Image", License: "apache-2.0",
		Description: "Photographic detail in eight steps; shares klein's text encoder.",
		Media: xollama.Media{Image: &xollama.ImageMedia{
			Model:    "hf.co/leejet/Z-Image-Turbo-GGUF/z_image_turbo-Q4_0.gguf",
			VAE:      "hf.co/Comfy-Org/z_image_turbo/split_files/vae/ae.safetensors",
			LLM:      qwen3_4B, // edit by the engine's choice, img2img for Z-Image
			Defaults: &xollama.ImageDefaults{Width: 1024, Height: 1024, Steps: 8, CFG: f(1), OutputFormat: "png"},
		}},
	},
	{
		ID: "whisper-large-v3-turbo", Template: "mannix/whisper:large-v3-turbo", Published: true, Name: "Whisper large-v3 turbo", Kind: "stt", Family: "Whisper", License: "mit",
		Description: "Transcription in 99 languages, and translation to English.",
		Media:       xollama.Media{STT: whisper()},
	},
	{
		ID: "outetts-0.3-500m", Template: "mannix/outetts:0.3", Published: true, Name: "OuteTTS 0.3 500M", Kind: "tts", Family: "OuteTTS", License: "cc-by-sa-4.0",
		Description: "Speech with the WavTokenizer vocoder; the pair opencoti b97 was tested with.",
		Media: xollama.Media{TTS: &xollama.TTSMedia{
			Model: "hf.co/OuteAI/OuteTTS-0.3-500M-GGUF/OuteTTS-0.3-500M-Q8_0.gguf",
			// No response_format: b97 answers wav only, and M7 makes mp3 the
			// engine's own default, so the engine's default is right on both.
			Vocoder: "hf.co/ggml-org/WavTokenizer/WavTokenizer-Large-75-F16.gguf",
		}},
	},
	{
		ID: "kokoro-82m", Template: "mannix/kokoro:82m", Name: "Kokoro 82M", Kind: "tts", Family: "Kokoro", License: "apache-2.0",
		Description: "46 natural voices in eight languages; SurfSense's default voice.",
		Needs:       needsM7, Media: xollama.Media{TTS: kokoro()},
	},
	{
		ID: "supertonic-3", Template: "mannix/supertonic:3", Name: "Supertonic 3", Kind: "tts", Family: "Supertonic", License: "openrail",
		Description: "Ten voices, each in 31 languages, in the least memory.",
		Needs:       needsM7,
		Media: xollama.Media{TTS: &xollama.TTSMedia{
			Engine: "audiocpp", Model: audioCppHub + "Supertonic-3-GGUF/supertonic-3-f16.gguf",
			VoiceMap: supertonicVoices,
			Defaults: &xollama.TTSDefaults{Voice: "F1", ResponseFormat: "mp3"},
		}},
	},
	{
		ID: "kitten-tts-mini-0.8", Template: "mannix/kittentts:mini-0.8", Name: "KittenTTS Mini 0.8", Kind: "tts", Family: "KittenTTS", License: "apache-2.0",
		Description: "Eight English voices.",
		Needs:       needsM7,
		Media: xollama.Media{TTS: &xollama.TTSMedia{
			Engine: "audiocpp", Model: audioCppHub + "KittenTTS-GGUF/kitten-tts-mini-0.8-orig.gguf",
			VoiceMap: kittenVoices,
			Defaults: &xollama.TTSDefaults{Voice: "Bella", ResponseFormat: "mp3"},
		}},
	},
	{
		ID: "wan2.1-t2v-1.3b", Template: "mannix/wan2.1:t2v-1.3b", Published: true, Name: "Wan2.1 T2V 1.3B", Kind: "video", Family: "Wan", License: "apache-2.0",
		Description: "Short clips from text, in the least memory.",
		Needs:       needsM7,
		Media: xollama.Media{Video: &xollama.VideoMedia{
			Model:       "hf.co/samuelchristlie/Wan2.1-T2V-1.3B-GGUF/Wan2.1-T2V-1.3B-Q8_0.gguf",
			VAE:         "hf.co/Comfy-Org/Wan_2.1_ComfyUI_repackaged/split_files/vae/wan_2.1_vae.safetensors",
			TextEncoder: umt5, Defaults: wanDefaults(640, 352, 16),
			MediaEngine: xollama.MediaEngine{ReserveMiB: wan21Reserve},
		}},
	},
	{
		ID: "wan2.2-ti2v-5b", Template: "mannix/wan2.2:ti2v-5b", Name: "Wan2.2 TI2V 5B", Kind: "video", Family: "Wan", License: "apache-2.0",
		Description: "Clips from text or an image, at 24 frames a second.",
		Needs:       needsM7,
		Media: xollama.Media{Video: &xollama.VideoMedia{
			Model:       "hf.co/QuantStack/Wan2.2-TI2V-5B-GGUF/Wan2.2-TI2V-5B-Q4_K_M.gguf",
			VAE:         "hf.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged/split_files/vae/wan2.2_vae.safetensors",
			TextEncoder: umt5, Defaults: wanDefaults(832, 480, 24),
		}},
	},
	{
		ID: "media-kit", Template: "mannix/media-kit:latest", Name: "Media kit", Kind: "mix", Family: "mix", License: "see components",
		Description: "FLUX.2 klein, Whisper turbo and Kokoro in one template: images, transcription and speech.",
		Needs:       needsM7,
		Media:       xollama.Media{Image: klein(), STT: whisper(), TTS: kokoro()},
	},
}

// Catalog returns every entry, each a copy the caller may change.
func Catalog() []Entry {
	out := make([]Entry, len(catalog))
	for i, e := range catalog {
		e.Media = *e.Media.Clone()
		out[i] = e
	}
	return out
}

// Find returns the entry with that id.
func Find(id string) (Entry, bool) {
	for _, e := range Catalog() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
