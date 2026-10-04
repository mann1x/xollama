package tweak

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/types/xollama"
)

// The media rows (plans/media-integration.md): opencoti's image, speech-to-
// text, text-to-speech and video engines attached to a model. Each kind's
// model is its switch: `--image` alone walks the image questions, and every
// other image question is quiet until a model is set.
//
// A component is typed as a file path or a sha256 digest. A path is hashed
// where it is typed and uploaded by write, so the config only ever holds
// digests and --dry-run sends nothing.

// mediaFiles maps a digest to the local file it was hashed from, and
// mediaSources a digest to the Hugging Face file it was resolved from, for
// write to send or have the server fetch. One run per process, so package
// variables are enough.
var (
	mediaFiles   = map[string]string{}
	mediaSources = map[string]string{}
)

// hubResolve names a Hugging Face file's digest; tests swap it.
var hubResolve = func(ctx context.Context, r mediahub.Ref) (mediahub.File, error) {
	return mediahub.Resolve(ctx, &http.Client{Timeout: 30 * time.Second}, r)
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// setBlob takes a file path, a digest, or unset.
func setBlob(v string, dst *string) error {
	s := strings.TrimSpace(v)
	switch strings.ToLower(s) {
	case "", "unset", "clear", "default", "none":
		*dst = ""
		return nil
	}
	if strings.HasPrefix(s, "sha256:") {
		s = strings.ToLower(s)
		if !digestRE.MatchString(s) {
			return fmt.Errorf("%q is not a sha256 digest", v)
		}
		*dst = s
		return nil
	}
	if strings.HasPrefix(s, "hf.co/") || strings.Contains(s, "huggingface.co/") {
		ref, err := mediahub.ParseRef(s)
		if err != nil {
			return err
		}
		f, err := hubResolve(context.Background(), ref)
		if err != nil {
			return err
		}
		mediaSources[f.Digest] = ref.String()
		*dst = f.Digest
		return nil
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		s = filepath.Join(home, rest)
	}
	d, err := hashFile(s)
	if err != nil {
		return err
	}
	mediaFiles[d] = s
	*dst = d
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; give the file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// uploadMedia gets the server every component it does not have yet: a local
// file is uploaded, a Hugging Face file is fetched by the server itself. A
// digest typed as such is the operator's word that the server has it; create
// refuses it otherwise.
func uploadMedia(ctx context.Context, client *api.Client, cfg *xollama.Config, out io.Writer) error {
	for _, c := range cfg.Media.Components() {
		path, local := mediaFiles[c.Digest]
		source, remote := mediaSources[c.Digest]
		if !local && !remote {
			continue
		}
		have, err := client.HeadBlob(ctx, c.Digest)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		if have {
			fmt.Fprintf(out, "   %s is already on the server\n", c.Name)
			continue
		}
		if !local {
			if err := pullMedia(ctx, client, c.Name, source, c.Digest, out); err != nil {
				return err
			}
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		fmt.Fprintf(out, "   uploading %s (%s, %s)\n", c.Name, filepath.Base(path), format.HumanBytes(fi.Size()))
		err = client.CreateBlob(ctx, c.Digest, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("upload %s: %w", c.Name, err)
		}
	}
	return nil
}

// pullMedia has the server fetch one Hugging Face file, reporting progress
// every quarter.
func pullMedia(ctx context.Context, client *api.Client, name, source, digest string, out io.Writer) error {
	fmt.Fprintf(out, "   fetching %s from %s\n", name, source)
	next := int64(25)
	err := client.MediaPull(ctx, &api.MediaPullRequest{Source: source, Digest: digest}, func(p api.ProgressResponse) error {
		if p.Total > 0 && p.Completed*100/p.Total >= next {
			fmt.Fprintf(out, "   %s %d%% of %s\n", name, p.Completed*100/p.Total, format.HumanBytes(p.Total))
			for next <= p.Completed*100/p.Total {
				next += 25
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("fetch %s: %w", name, err)
	}
	return nil
}

// mediaKind is one media engine's place in the config: how to read it without
// creating it, and how to reach it for a write.
type mediaKind[T any] struct {
	name   string
	what   string
	read   func(*xollama.Media) *T
	make   func(*xollama.Media) *T
	engine func(*T) *xollama.MediaEngine
	model  func(*T) string
}

func mediaOf(c *xollama.Config) *xollama.Media {
	if c.Media == nil {
		c.Media = &xollama.Media{}
	}
	return c.Media
}

func (k mediaKind[T]) get(c *xollama.Config) *T {
	if c.Media == nil {
		return nil
	}
	return k.read(c.Media)
}

// off blocks a kind's settings until its model is set: without one there is
// no engine for them to configure.
func (k mediaKind[T]) off(c *xollama.Config) string {
	if t := k.get(c); t != nil && k.model(t) != "" {
		return ""
	}
	return fmt.Sprintf("a media.%s setting, and media.%s.model is not set", k.name, k.name)
}

// row builds one field of the kind. read must not create anything; write may.
func (k mediaKind[T]) row(name, path, title, help string, fk kind, choices []string, read func(*T) string, write func(*T, string) error) field {
	f := field{
		name:  k.name + "-" + name,
		path:  "media." + k.name + "." + path,
		title: title,
		help:  help,
		kind:  fk,
		get: func(c *xollama.Config) string {
			if t := k.get(c); t != nil {
				return read(t)
			}
			return ""
		},
		set: func(c *xollama.Config, v string) error {
			return write(k.make(mediaOf(c)), v)
		},
		blocked: k.off,
		quiet:   true,
	}
	if choices != nil {
		f.choices = func(*xollama.Config) []string { return choices }
	}
	return f
}

// engineRows are the rows every kind shares.
func (k mediaKind[T]) engineRows(defaults []string) []field {
	e := func(t *T) *xollama.MediaEngine { return k.engine(t) }
	return []field{
		k.row("device", "device", "Device for the "+k.what+" engine",
			"The engine's device, as the engine names it (CUDA0, Vulkan1). Unset lets the\nengine place it.",
			kindText, nil,
			func(t *T) string { return e(t).Device },
			func(t *T, v string) error { return setText(v, &e(t).Device) }),
		k.row("reserve", "reserve_mib", "Memory set aside for the "+k.what+" engine",
			"Added to the fit target on its device. Unset is the engine's default.",
			kindInt, nil,
			func(t *T) string { return showInt(e(t).ReserveMiB) },
			func(t *T, v string) error { return setInt(v, &e(t).ReserveMiB) }),
		k.row("args", "args", "Further "+k.what+" engine flags",
			"Flags for a family setting this schema has no field for, separated by spaces.\nUnset passes none.",
			kindText, nil,
			func(t *T) string { return strings.Join(e(t).Args, " ") },
			func(t *T, v string) error {
				var s string
				if err := setText(v, &s); err != nil {
					return err
				}
				e(t).Args = strings.Fields(s)
				return nil
			}),
		k.row("fixed", "fixed", "Settings a request may not override",
			"Comma-separated names from: "+strings.Join(defaults, ", ")+".\n"+
				"The template's value then applies even when a client sends its own. Unset\n"+
				"lets a client's value win, which is the normal answer.",
			kindText, nil,
			func(t *T) string { return strings.Join(e(t).Fixed, ",") },
			func(t *T, v string) error {
				var s string
				if err := setText(v, &s); err != nil {
					return err
				}
				e(t).Fixed = splitList(s)
				return nil
			}),
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func showFloatPtr(f *float64) string {
	if f == nil {
		return ""
	}
	return showFloat(*f)
}

func showSeed(n *int64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatInt(*n, 10)
}

func setChoice(v string, valid []string, dst *string) error {
	s, err := choice(v, valid)
	if err != nil {
		return err
	}
	*dst = s
	return nil
}

var imageKind = mediaKind[xollama.ImageMedia]{
	name: "image", what: "image",
	read: func(m *xollama.Media) *xollama.ImageMedia { return m.Image },
	make: func(m *xollama.Media) *xollama.ImageMedia {
		if m.Image == nil {
			m.Image = &xollama.ImageMedia{}
		}
		return m.Image
	},
	engine: func(t *xollama.ImageMedia) *xollama.MediaEngine { return &t.MediaEngine },
	model:  func(t *xollama.ImageMedia) string { return t.Model },
}

var sttKind = mediaKind[xollama.STTMedia]{
	name: "stt", what: "speech-to-text",
	read: func(m *xollama.Media) *xollama.STTMedia { return m.STT },
	make: func(m *xollama.Media) *xollama.STTMedia {
		if m.STT == nil {
			m.STT = &xollama.STTMedia{}
		}
		return m.STT
	},
	engine: func(t *xollama.STTMedia) *xollama.MediaEngine { return &t.MediaEngine },
	model:  func(t *xollama.STTMedia) string { return t.Model },
}

var ttsKind = mediaKind[xollama.TTSMedia]{
	name: "tts", what: "text-to-speech",
	read: func(m *xollama.Media) *xollama.TTSMedia { return m.TTS },
	make: func(m *xollama.Media) *xollama.TTSMedia {
		if m.TTS == nil {
			m.TTS = &xollama.TTSMedia{}
		}
		return m.TTS
	},
	engine: func(t *xollama.TTSMedia) *xollama.MediaEngine { return &t.MediaEngine },
	model:  func(t *xollama.TTSMedia) string { return t.Model },
}

var videoKind = mediaKind[xollama.VideoMedia]{
	name: "video", what: "video",
	read: func(m *xollama.Media) *xollama.VideoMedia { return m.Video },
	make: func(m *xollama.Media) *xollama.VideoMedia {
		if m.Video == nil {
			m.Video = &xollama.VideoMedia{}
		}
		return m.Video
	},
	engine: func(t *xollama.VideoMedia) *xollama.MediaEngine { return &t.MediaEngine },
	model:  func(t *xollama.VideoMedia) string { return t.Model },
}

// blobHelp closes every component question.
const blobHelp = "\nA file path is uploaded on write; an hf.co/<owner>/<repo>/<file> reference is fetched\n" +
	"by the server from Hugging Face; a sha256 digest names a blob the server has."

// head is a kind's model row: its switch, and the flag that scopes the walk
// to the kind. It is asked on every walk, and Enter leaves it unset.
func head[T any](k mediaKind[T], title, help string, at func(*T) *string, group []field) field {
	names := []string{k.name}
	for _, f := range group {
		names = append(names, f.name)
	}
	return field{
		name: k.name, path: "media." + k.name + ".model",
		title: title, help: help + blobHelp,
		kind: kindBlob, head: true, group: names,
		get: func(c *xollama.Config) string {
			if t := k.get(c); t != nil {
				return *at(t)
			}
			return ""
		},
		set: func(c *xollama.Config, v string) error { return setBlob(v, at(k.make(mediaOf(c)))) },
	}
}

func blobRow[T any](k mediaKind[T], name, path, title, help string, at func(*T) *string) field {
	return k.row(name, path, title, help+blobHelp,
		kindBlob, nil,
		func(t *T) string { return *at(t) },
		func(t *T, v string) error { return setBlob(v, at(t)) })
}

func imageFields() []field {
	k := imageKind
	d := func(t *xollama.ImageMedia) *xollama.ImageDefaults {
		if t.Defaults == nil {
			t.Defaults = &xollama.ImageDefaults{}
		}
		return t.Defaults
	}
	r := func(t *xollama.ImageMedia) xollama.ImageDefaults {
		if t.Defaults == nil {
			return xollama.ImageDefaults{}
		}
		return *t.Defaults
	}
	rows := []field{
		blobRow(k, "vae", "vae", "Image VAE", "The family's VAE (--diffusion-vae), when the model file does not carry one.",
			func(t *xollama.ImageMedia) *string { return &t.VAE }),
		blobRow(k, "llm", "llm", "Image text encoder", "The family's text encoder (--diffusion-llm): Qwen3-4B for FLUX.2 Klein 4B.",
			func(t *xollama.ImageMedia) *string { return &t.LLM }),
		blobRow(k, "llm-vision", "llm_vision", "Image text encoder's vision projector",
			"--diffusion-llm-vision, for a family whose edits read the image through the\ntext encoder (Qwen-Image-Edit). FLUX.2 Klein needs none.",
			func(t *xollama.ImageMedia) *string { return &t.LLMVision }),
		k.row("edit", "edit", "How an image edit uses its input",
			"reference: an instruction edit from reference images (FLUX.2, Qwen-Image).\n"+
				"img2img: re-noise the input. none: refuse /v1/images/edits.\n"+
				"Unset lets the engine decide by family.",
			kindChoice, xollama.ValidImageEdit(),
			func(t *xollama.ImageMedia) string { return t.Edit },
			func(t *xollama.ImageMedia, v string) error { return setChoice(v, xollama.ValidImageEdit(), &t.Edit) }),
	}
	rows = append(rows, k.engineRows([]string{"width", "height", "steps", "cfg", "sampler", "scheduler", "flow_shift", "seed", "output_format", "strength"})...)
	defHelp := "\nApplied when the engine starts and to every request that leaves it out."
	rows = append(rows,
		k.row("width", "defaults.width", "Image width", "In pixels."+defHelp, kindInt, nil,
			func(t *xollama.ImageMedia) string { return showInt(r(t).Width) },
			func(t *xollama.ImageMedia, v string) error { return setInt(v, &d(t).Width) }),
		k.row("height", "defaults.height", "Image height", "In pixels."+defHelp, kindInt, nil,
			func(t *xollama.ImageMedia) string { return showInt(r(t).Height) },
			func(t *xollama.ImageMedia, v string) error { return setInt(v, &d(t).Height) }),
		k.row("steps", "defaults.steps", "Sampling steps", "FLUX.2 Klein is distilled for 4."+defHelp, kindInt, nil,
			func(t *xollama.ImageMedia) string { return showInt(r(t).Steps) },
			func(t *xollama.ImageMedia, v string) error { return setInt(v, &d(t).Steps) }),
		k.row("cfg", "defaults.cfg", "Guidance scale", "Distilled models want 1."+defHelp, kindFloat, nil,
			func(t *xollama.ImageMedia) string { return showFloatPtr(r(t).CFG) },
			func(t *xollama.ImageMedia, v string) error { return setFloatPtr(v, &d(t).CFG) }),
		k.row("sampler", "defaults.sampler", "Sampler", "stable-diffusion.cpp's name for it (euler, euler_a, dpm++2m, ...)."+defHelp, kindText, nil,
			func(t *xollama.ImageMedia) string { return r(t).Sampler },
			func(t *xollama.ImageMedia, v string) error { return setText(v, &d(t).Sampler) }),
		k.row("scheduler", "defaults.scheduler", "Scheduler", "stable-diffusion.cpp's name for it (discrete, karras, simple, ...)."+defHelp, kindText, nil,
			func(t *xollama.ImageMedia) string { return r(t).Scheduler },
			func(t *xollama.ImageMedia, v string) error { return setText(v, &d(t).Scheduler) }),
		k.row("flow-shift", "defaults.flow_shift", "Flow shift", "For flow-matching families."+defHelp, kindFloat, nil,
			func(t *xollama.ImageMedia) string { return showFloatPtr(r(t).FlowShift) },
			func(t *xollama.ImageMedia, v string) error { return setFloatPtr(v, &d(t).FlowShift) }),
		k.row("seed", "defaults.seed", "Seed", "A fixed seed makes every prompt reproducible; `random` (unset) draws one."+defHelp, kindInt, nil,
			func(t *xollama.ImageMedia) string { return showSeed(r(t).Seed) },
			func(t *xollama.ImageMedia, v string) error { return setSeed(v, &d(t).Seed) }),
		k.row("format", "defaults.output_format", "Image format", "What the engine encodes."+defHelp, kindChoice, xollama.ValidImageFormats(),
			func(t *xollama.ImageMedia) string { return r(t).OutputFormat },
			func(t *xollama.ImageMedia, v string) error {
				return setChoice(v, xollama.ValidImageFormats(), &d(t).OutputFormat)
			}),
		k.row("strength", "defaults.strength", "Edit strength", "How far an img2img edit moves from its input, above 0 and up to 1."+defHelp, kindFloat, nil,
			func(t *xollama.ImageMedia) string { return showFloatPtr(r(t).Strength) },
			func(t *xollama.ImageMedia, v string) error { return setFloatPtr(v, &d(t).Strength) }),
	)
	return append([]field{head(k, "Image model — generation and edit (/v1/images/*)",
		"The diffusion model (--diffusion-model), for example FLUX.2 Klein 4B.\nUnset leaves the model without an image engine.",
		func(t *xollama.ImageMedia) *string { return &t.Model }, rows)}, rows...)
}

func sttFields() []field {
	k := sttKind
	d := func(t *xollama.STTMedia) *xollama.STTDefaults {
		if t.Defaults == nil {
			t.Defaults = &xollama.STTDefaults{}
		}
		return t.Defaults
	}
	r := func(t *xollama.STTMedia) xollama.STTDefaults {
		if t.Defaults == nil {
			return xollama.STTDefaults{}
		}
		return *t.Defaults
	}
	defHelp := "\nApplied to every request that leaves it out."
	rows := []field{
		k.row("threads", "threads", "Speech-to-text threads", "--stt-threads. Unset is the engine's default.", kindInt, nil,
			func(t *xollama.STTMedia) string { return showInt(t.Threads) },
			func(t *xollama.STTMedia, v string) error { return setInt(v, &t.Threads) }),
	}
	rows = append(rows, k.engineRows([]string{"language", "response_format", "task"})...)
	rows = append(rows,
		k.row("language", "defaults.language", "Spoken language", "An ISO-639-1 code (en, it). Unset lets the model detect it."+defHelp, kindText, nil,
			func(t *xollama.STTMedia) string { return r(t).Language },
			func(t *xollama.STTMedia, v string) error { return setText(v, &d(t).Language) }),
		k.row("format", "defaults.response_format", "Transcript format", "OpenAI clients that send none expect json."+defHelp, kindChoice, xollama.ValidSTTFormats(),
			func(t *xollama.STTMedia) string { return r(t).ResponseFormat },
			func(t *xollama.STTMedia, v string) error {
				return setChoice(v, xollama.ValidSTTFormats(), &d(t).ResponseFormat)
			}),
		k.row("task", "defaults.task", "Transcribe or translate", "translate answers in English."+defHelp, kindChoice, xollama.ValidSTTTasks(),
			func(t *xollama.STTMedia) string { return r(t).Task },
			func(t *xollama.STTMedia, v string) error { return setChoice(v, xollama.ValidSTTTasks(), &d(t).Task) }),
	)
	return append([]field{head(k, "Speech-to-text model (/v1/audio/transcriptions)",
		"A transcribe.cpp GGUF (Whisper, Parakeet, Canary, ...) or a whisper.cpp ggml-*.bin.\n"+
			"Unset leaves transcription to upstream's path: an audio LLM's chat.",
		func(t *xollama.STTMedia) *string { return &t.Model }, rows)}, rows...)
}

func ttsFields() []field {
	k := ttsKind
	d := func(t *xollama.TTSMedia) *xollama.TTSDefaults {
		if t.Defaults == nil {
			t.Defaults = &xollama.TTSDefaults{}
		}
		return t.Defaults
	}
	r := func(t *xollama.TTSMedia) xollama.TTSDefaults {
		if t.Defaults == nil {
			return xollama.TTSDefaults{}
		}
		return *t.Defaults
	}
	defHelp := "\nApplied to every request that leaves it out."
	rows := []field{
		k.row("engine", "engine", "Text-to-speech engine",
			"outetts: OuteTTS with its WavTokenizer vocoder (the default).\naudiocpp: Kokoro, Supertonic, KittenTTS.",
			kindChoice, xollama.ValidTTSEngines(),
			func(t *xollama.TTSMedia) string { return t.Engine },
			func(t *xollama.TTSMedia, v string) error { return setChoice(v, xollama.ValidTTSEngines(), &t.Engine) }),
		blobRow(k, "vocoder", "vocoder", "OuteTTS vocoder", "The WavTokenizer GGUF (--tts-vocoder); OuteTTS cannot speak without it.",
			func(t *xollama.TTSMedia) *string { return &t.Vocoder }),
		k.row("voices", "voices", "Extra voices",
			"name=file pairs, comma-separated: narrator=~/voices/narrator.json. Each file is one\n"+
				"voice (an OuteTTS speaker JSON), stored as its own layer and selected by its name.\n"+
				"A file is a local path, an hf.co reference or a sha256 digest.",
			kindText, nil,
			func(t *xollama.TTSMedia) string { return showVoiceMap(t.Voices) },
			func(t *xollama.TTSMedia, v string) error { return setVoices(v, &t.Voices) }),
		k.row("voice-map", "voice_map", "Voice names a client may use",
			"name=voice pairs, comma-separated: alloy=af_heart,nova=af_bella. OpenAI clients\n"+
				"ask for alloy, echo, fable, onyx, nova or shimmer. Unset passes names through.",
			kindText, nil,
			func(t *xollama.TTSMedia) string { return showVoiceMap(t.VoiceMap) },
			func(t *xollama.TTSMedia, v string) error { return setVoiceMap(v, &t.VoiceMap) }),
	}
	rows = append(rows, k.engineRows([]string{"voice", "language", "response_format", "speed"})...)
	rows = append(rows,
		k.row("voice", "defaults.voice", "Voice", "One of the model's own voices."+defHelp, kindText, nil,
			func(t *xollama.TTSMedia) string { return r(t).Voice },
			func(t *xollama.TTSMedia, v string) error { return setText(v, &d(t).Voice) }),
		k.row("language", "defaults.language", "Language", "For a multilingual voice model."+defHelp, kindText, nil,
			func(t *xollama.TTSMedia) string { return r(t).Language },
			func(t *xollama.TTSMedia, v string) error { return setText(v, &d(t).Language) }),
		k.row("format", "defaults.response_format", "Audio format", "OpenAI clients that send none expect mp3."+defHelp, kindChoice, xollama.ValidTTSFormats(),
			func(t *xollama.TTSMedia) string { return r(t).ResponseFormat },
			func(t *xollama.TTSMedia, v string) error {
				return setChoice(v, xollama.ValidTTSFormats(), &d(t).ResponseFormat)
			}),
		k.row("speed", "defaults.speed", "Speaking speed", "0.25 to 4, 1 is normal."+defHelp, kindFloat, nil,
			func(t *xollama.TTSMedia) string { return showFloatPtr(r(t).Speed) },
			func(t *xollama.TTSMedia, v string) error { return setFloatPtr(v, &d(t).Speed) }),
	)
	return append([]field{head(k, "Text-to-speech model (/v1/audio/speech)",
		"OuteTTS, or with the audiocpp engine Kokoro, Supertonic or KittenTTS.\nUnset leaves the model without a voice.",
		func(t *xollama.TTSMedia) *string { return &t.Model }, rows)}, rows...)
}

func showVoiceMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}

func setVoiceMap(v string, dst *map[string]string) error {
	var s string
	if err := setText(v, &s); err != nil {
		return err
	}
	if s == "" {
		*dst = nil
		return nil
	}
	out := map[string]string{}
	for _, p := range splitList(s) {
		name, voice, ok := strings.Cut(p, "=")
		name, voice = strings.TrimSpace(name), strings.TrimSpace(voice)
		if !ok || name == "" || voice == "" {
			return fmt.Errorf("want name=voice pairs (got %q)", p)
		}
		out[name] = voice
	}
	*dst = out
	return nil
}

// setVoices reads name=file pairs, each file through setBlob, so a voice is
// uploaded, fetched or named by digest like any other component.
func setVoices(v string, dst *map[string]string) error {
	var s string
	if err := setText(v, &s); err != nil {
		return err
	}
	if s == "" {
		*dst = nil
		return nil
	}
	out := map[string]string{}
	for _, p := range splitList(s) {
		name, file, ok := strings.Cut(p, "=")
		name, file = strings.TrimSpace(name), strings.TrimSpace(file)
		if !ok || name == "" || file == "" {
			return fmt.Errorf("want name=file pairs (got %q)", p)
		}
		if _, dup := out[name]; dup {
			return fmt.Errorf("voice %q is given twice", name)
		}
		var digest string
		if err := setBlob(file, &digest); err != nil {
			return fmt.Errorf("voice %s: %w", name, err)
		}
		out[name] = digest
	}
	*dst = out
	return nil
}

func videoFields() []field {
	k := videoKind
	d := func(t *xollama.VideoMedia) *xollama.VideoDefaults {
		if t.Defaults == nil {
			t.Defaults = &xollama.VideoDefaults{}
		}
		return t.Defaults
	}
	r := func(t *xollama.VideoMedia) xollama.VideoDefaults {
		if t.Defaults == nil {
			return xollama.VideoDefaults{}
		}
		return *t.Defaults
	}
	defHelp := "\nApplied when the engine starts and to every request that leaves it out."
	ints := []struct {
		name, path, title string
		at                func(*xollama.VideoDefaults) *int
	}{
		{"width", "defaults.width", "Video width", func(d *xollama.VideoDefaults) *int { return &d.Width }},
		{"height", "defaults.height", "Video height", func(d *xollama.VideoDefaults) *int { return &d.Height }},
		{"frames", "defaults.frames", "Frames per clip", func(d *xollama.VideoDefaults) *int { return &d.Frames }},
		{"fps", "defaults.fps", "Frames per second", func(d *xollama.VideoDefaults) *int { return &d.FPS }},
		{"steps", "defaults.steps", "Sampling steps", func(d *xollama.VideoDefaults) *int { return &d.Steps }},
	}
	rows := []field{
		blobRow(k, "vae", "vae", "Video VAE", "The family's VAE: wan_2.1_vae, or wan2.2_vae for the TI2V 5B.",
			func(t *xollama.VideoMedia) *string { return &t.VAE }),
		blobRow(k, "text-encoder", "text_encoder", "Video text encoder", "umt5-xxl for Wan.",
			func(t *xollama.VideoMedia) *string { return &t.TextEncoder }),
	}
	rows = append(rows, k.engineRows([]string{"width", "height", "frames", "fps", "steps", "cfg", "sampler", "flow_shift", "seed", "output_format"})...)
	for _, i := range ints {
		rows = append(rows, k.row(i.name, i.path, i.title, "A whole number."+defHelp, kindInt, nil,
			func(t *xollama.VideoMedia) string { r := r(t); return showInt(*i.at(&r)) },
			func(t *xollama.VideoMedia, v string) error { return setInt(v, i.at(d(t))) }))
	}
	rows = append(rows,
		k.row("cfg", "defaults.cfg", "Guidance scale", "Wan uses 6."+defHelp, kindFloat, nil,
			func(t *xollama.VideoMedia) string { return showFloatPtr(r(t).CFG) },
			func(t *xollama.VideoMedia, v string) error { return setFloatPtr(v, &d(t).CFG) }),
		k.row("sampler", "defaults.sampler", "Sampler", "stable-diffusion.cpp's name for it."+defHelp, kindText, nil,
			func(t *xollama.VideoMedia) string { return r(t).Sampler },
			func(t *xollama.VideoMedia, v string) error { return setText(v, &d(t).Sampler) }),
		k.row("flow-shift", "defaults.flow_shift", "Flow shift", "Wan uses 3."+defHelp, kindFloat, nil,
			func(t *xollama.VideoMedia) string { return showFloatPtr(r(t).FlowShift) },
			func(t *xollama.VideoMedia, v string) error { return setFloatPtr(v, &d(t).FlowShift) }),
		k.row("seed", "defaults.seed", "Seed", "`random` (unset) draws one per clip."+defHelp, kindInt, nil,
			func(t *xollama.VideoMedia) string { return showSeed(r(t).Seed) },
			func(t *xollama.VideoMedia, v string) error { return setSeed(v, &d(t).Seed) }),
		k.row("format", "defaults.output_format", "Video container", "Browsers play mp4 and webm."+defHelp, kindChoice, xollama.ValidVideoFormats(),
			func(t *xollama.VideoMedia) string { return r(t).OutputFormat },
			func(t *xollama.VideoMedia, v string) error {
				return setChoice(v, xollama.ValidVideoFormats(), &d(t).OutputFormat)
			}),
	)
	return append([]field{head(k, "Video model (/v1/videos) — served once the engine has video",
		"A Wan model, for example Wan2.1 T2V 1.3B. The template can be written now; a\nload is refused until the engine advertises video.",
		func(t *xollama.VideoMedia) *string { return &t.Model }, rows)}, rows...)
}

func init() {
	fields = append(fields, slices.Concat(imageFields(), sttFields(), ttsFields(), videoFields())...)
}
