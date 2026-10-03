package xollama

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Media attaches opencoti's media engines to a model: image generation and
// edit, speech-to-text, text-to-speech, and later video
// (plans/media-integration.md). A model may carry any mix of them beside its
// LLM, or be a template that carries only media.
//
// Components are blobs named here by digest. The create path turns each one
// into a manifest layer of MediaTypeMedia, named by role (MediaLayerName), so
// push, pull and blob GC carry it like any other layer; stock ollama skips the
// media type, since its layer switch has no default case. The layers are never
// read back: the digests below are the record, and a load finds each blob by
// its digest.
//
// THE TEMPLATE OWNS THE ENGINE'S SETTINGS (owner, 2026-10-02). Each kind has
// Defaults: they boot the engine, and xollama fills every field a request
// leaves out from them before it forwards the request -- clients such as
// SurfSense post only a prompt. A client's own value wins unless its field is
// listed in Fixed.
type Media struct {
	Image *ImageMedia `json:"image,omitempty"`
	STT   *STTMedia   `json:"stt,omitempty"`
	TTS   *TTSMedia   `json:"tts,omitempty"`
	Video *VideoMedia `json:"video,omitempty"`
}

// MediaTypeMedia is the manifest media type of a media component layer. It is
// xollama's own: reusing upstream's image.model would make a VAE or a vocoder
// read as the model's LLM weights.
const MediaTypeMedia = "application/vnd.xollama.media"

// MediaLayerName names a component's layer: "media/<kind>.<role>".
func MediaLayerName(kind, role string) string { return "media/" + kind + "." + role }

// ImageMedia is the image engine (stable-diffusion.cpp): generation, and edit
// when Edit allows it.
type ImageMedia struct {
	// Model is the diffusion model (--diffusion-model). Required.
	Model string `json:"model"`
	// VAE, LLM and LLMVision are the family's companions: the VAE, the text
	// encoder (--diffusion-llm) and its vision projector
	// (--diffusion-llm-vision), each a digest.
	VAE       string `json:"vae,omitempty"`
	LLM       string `json:"llm,omitempty"`
	LLMVision string `json:"llm_vision,omitempty"`
	// Edit is how /v1/images/edits uses the input image: "reference"
	// (instruction edit, FLUX.2 and Qwen-Image), "img2img", or "none" to
	// refuse edits. Empty lets the engine decide by family.
	Edit string `json:"edit,omitempty"`
	MediaEngine
	Defaults *ImageDefaults `json:"defaults,omitempty"`
}

// ImageDefaults are an image model's settings, applied at launch and to every
// request that leaves them out.
type ImageDefaults struct {
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Steps        int      `json:"steps,omitempty"`
	CFG          *float64 `json:"cfg,omitempty"`
	Sampler      string   `json:"sampler,omitempty"`
	Scheduler    string   `json:"scheduler,omitempty"`
	FlowShift    *float64 `json:"flow_shift,omitempty"`
	Seed         *int64   `json:"seed,omitempty"`
	OutputFormat string   `json:"output_format,omitempty"`
	// Strength is how far an img2img edit moves from its input, 0 to 1.
	Strength *float64 `json:"strength,omitempty"`
}

// STTMedia is the speech-to-text engine (transcribe.cpp).
type STTMedia struct {
	// Model is a transcribe.cpp GGUF or a whisper.cpp ggml-*.bin. Required.
	Model string `json:"model"`
	// Threads is --stt-threads. Zero is the engine's default.
	Threads int `json:"threads,omitempty"`
	MediaEngine
	Defaults *STTDefaults `json:"defaults,omitempty"`
}

// STTDefaults are a speech-to-text model's request settings.
type STTDefaults struct {
	Language       string `json:"language,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	// Task is "transcribe" or "translate" (to English). It picks the route
	// a request without one is served as.
	Task string `json:"task,omitempty"`
}

// TTSMedia is the text-to-speech engine.
type TTSMedia struct {
	// Engine is "outetts" (OuteTTS + WavTokenizer, the default) or
	// "audiocpp" (Kokoro, Supertonic, KittenTTS), as the engine offers them.
	Engine string `json:"engine,omitempty"`
	// Model is the TTS model. Required.
	Model string `json:"model"`
	// Vocoder is OuteTTS's WavTokenizer; OuteTTS cannot speak without it.
	Vocoder string `json:"vocoder,omitempty"`
	// Voices are extra voices by name, each one file (an OuteTTS speaker
	// JSON, say) stored as its own layer and passed to the engine as
	// --tts-voice NAME=<blob>. The name is what a request's voice selects.
	Voices map[string]string `json:"voices,omitempty"`
	// VoiceMap maps a client's voice name to one of the model's: OpenAI
	// clients ask for alloy, echo, fable, onyx, nova or shimmer.
	VoiceMap map[string]string `json:"voice_map,omitempty"`
	MediaEngine
	Defaults *TTSDefaults `json:"defaults,omitempty"`
}

// TTSDefaults are a text-to-speech model's request settings.
type TTSDefaults struct {
	Voice          string   `json:"voice,omitempty"`
	Language       string   `json:"language,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Speed          *float64 `json:"speed,omitempty"`
}

// VideoMedia is the video engine. The schema is reserved now; a load is
// refused until the engine advertises video (opencoti #621).
type VideoMedia struct {
	Model string `json:"model"`
	VAE   string `json:"vae,omitempty"`
	// TextEncoder is the family's text encoder: umt5 for Wan.
	TextEncoder string `json:"text_encoder,omitempty"`
	MediaEngine
	Defaults *VideoDefaults `json:"defaults,omitempty"`
}

// VideoDefaults are a video model's settings.
type VideoDefaults struct {
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Frames       int      `json:"frames,omitempty"`
	FPS          int      `json:"fps,omitempty"`
	Steps        int      `json:"steps,omitempty"`
	CFG          *float64 `json:"cfg,omitempty"`
	Sampler      string   `json:"sampler,omitempty"`
	FlowShift    *float64 `json:"flow_shift,omitempty"`
	Seed         *int64   `json:"seed,omitempty"`
	OutputFormat string   `json:"output_format,omitempty"`
}

// MediaEngine holds what every media engine shares.
type MediaEngine struct {
	// Device is the engine's device (--diffusion-device, --stt-device,
	// --tts-device). Empty lets the engine place it.
	Device string `json:"device,omitempty"`
	// ReserveMiB is the memory set aside for the engine on its device. Zero
	// is the engine's default (image 3072, STT 512, TTS 256).
	ReserveMiB int `json:"reserve_mib,omitempty"`
	// Args are further engine flags, for a family setting this schema has no
	// field for (for example --diffusion-args "--vae-tiling").
	Args []string `json:"args,omitempty"`
	// Fixed lists the Defaults fields a request may not override.
	Fixed []string `json:"fixed,omitempty"`
}

// Closed sets, each taken from the route it describes.
var (
	validImageEdit    = []string{"reference", "img2img", "none"}
	validImageFormats = []string{"png", "jpeg"}
	validSTTFormats   = []string{"json", "text", "verbose_json", "srt", "vtt"}
	validSTTTasks     = []string{"transcribe", "translate"}
	validTTSEngines   = []string{"outetts", "audiocpp"}
	validTTSFormats   = []string{"mp3", "opus", "aac", "flac", "wav", "pcm"}
	validVideoFormats = []string{"mp4", "webm", "webp", "avi"}
	mediaDigest       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// voiceName is what the engine accepts as a --tts-voice name.
	voiceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	// reservedVoiceNames are OuteTTS's own: its default voice and the OpenAI
	// names it maps to it (opencoti #679).
	reservedVoiceNames   = []string{"default", "alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse"}
	imageDefaultFields   = []string{"width", "height", "steps", "cfg", "sampler", "scheduler", "flow_shift", "seed", "output_format", "strength"}
	sttDefaultFields     = []string{"language", "response_format", "task"}
	ttsDefaultFields     = []string{"voice", "language", "response_format", "speed"}
	videoDefaultFields   = []string{"width", "height", "frames", "fps", "steps", "cfg", "sampler", "flow_shift", "seed", "output_format"}
	maxMediaSide         = 8192
	maxMediaSteps        = 1000
	maxVideoFrames       = 2048
	maxVideoFPS          = 240
	minTTSSpeed, maxTTSS = 0.25, 4.0
)

// ValidImageEdit, ValidImageFormats and the rest return the closed sets, for a
// menu that offers exactly what Validate accepts.
func ValidImageEdit() []string    { return slices.Clone(validImageEdit) }
func ValidImageFormats() []string { return slices.Clone(validImageFormats) }
func ValidSTTFormats() []string   { return slices.Clone(validSTTFormats) }
func ValidSTTTasks() []string     { return slices.Clone(validSTTTasks) }
func ValidTTSEngines() []string   { return slices.Clone(validTTSEngines) }
func ValidTTSFormats() []string   { return slices.Clone(validTTSFormats) }
func ValidVideoFormats() []string { return slices.Clone(validVideoFormats) }

// IsZero reports whether m attaches nothing.
func (m *Media) IsZero() bool {
	return m == nil || (m.Image == nil && m.STT == nil && m.TTS == nil && m.Video == nil)
}

// MediaComponent is one blob a model's media needs, with the layer name it is
// stored under.
type MediaComponent struct {
	Name   string
	Digest string
}

// Components lists every blob the media names, in a stable order, so the
// create path writes the same manifest for the same config.
func (m *Media) Components() []MediaComponent {
	if m.IsZero() {
		return nil
	}
	var out []MediaComponent
	add := func(kind, role, digest string) {
		if digest != "" {
			out = append(out, MediaComponent{Name: MediaLayerName(kind, role), Digest: digest})
		}
	}
	if i := m.Image; i != nil {
		add("image", "model", i.Model)
		add("image", "vae", i.VAE)
		add("image", "llm", i.LLM)
		add("image", "llm_vision", i.LLMVision)
	}
	if s := m.STT; s != nil {
		add("stt", "model", s.Model)
	}
	if t := m.TTS; t != nil {
		add("tts", "model", t.Model)
		add("tts", "vocoder", t.Vocoder)
		names := make([]string, 0, len(t.Voices))
		for name := range t.Voices {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add("tts", "voices."+name, t.Voices[name])
		}
	}
	if v := m.Video; v != nil {
		add("video", "model", v.Model)
		add("video", "vae", v.VAE)
		add("video", "text_encoder", v.TextEncoder)
	}
	return out
}

// Kinds lists the media kinds m carries.
func (m *Media) Kinds() []string {
	if m.IsZero() {
		return nil
	}
	var out []string
	if m.Image != nil {
		out = append(out, "image")
	}
	if m.STT != nil {
		out = append(out, "stt")
	}
	if m.TTS != nil {
		out = append(out, "tts")
	}
	if m.Video != nil {
		out = append(out, "video")
	}
	return out
}

func (m *Media) validate(engine string) error {
	if m.IsZero() {
		return nil
	}
	// Stock llama.cpp has no media engine. A model that pins it and also
	// carries media asks for two things that cannot both be served.
	if engine == EngineLlamaCpp {
		return fmt.Errorf("xollama config: media needs the opencoti engine; this config pins engine %q", engine)
	}
	for _, c := range m.Components() {
		if !mediaDigest.MatchString(c.Digest) {
			return fmt.Errorf("xollama config: %s: %q is not a sha256 digest", mediaField(c.Name), c.Digest)
		}
	}
	if i := m.Image; i != nil {
		if err := i.validate(); err != nil {
			return err
		}
	}
	if s := m.STT; s != nil {
		if err := s.validate(); err != nil {
			return err
		}
	}
	if t := m.TTS; t != nil {
		if err := t.validate(); err != nil {
			return err
		}
	}
	if v := m.Video; v != nil {
		if err := v.validate(); err != nil {
			return err
		}
	}
	return nil
}

// mediaField turns a layer name back into the config field it came from.
func mediaField(layer string) string {
	return "media." + strings.TrimPrefix(layer, "media/")
}

func (e *MediaEngine) validate(kind string, stated func(string) bool, fields []string) error {
	if e.ReserveMiB < 0 {
		return fmt.Errorf("xollama config: media.%s.reserve_mib %d must not be negative", kind, e.ReserveMiB)
	}
	for _, a := range e.Args {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("xollama config: media.%s.args has an empty argument", kind)
		}
	}
	seen := map[string]bool{}
	for _, f := range e.Fixed {
		if !slices.Contains(fields, f) {
			return fmt.Errorf("xollama config: media.%s.fixed: unknown field %q (want one of %v)", kind, f, fields)
		}
		if seen[f] {
			return fmt.Errorf("xollama config: media.%s.fixed lists %q twice", kind, f)
		}
		seen[f] = true
		// Fixing a value the template does not state fixes nothing: the
		// request would get the engine's default with no way to change it.
		if !stated(f) {
			return fmt.Errorf("xollama config: media.%s.fixed: %q has no value in media.%s.defaults to fix", kind, f, kind)
		}
	}
	return nil
}

func positive(kind, field string, v, maxV int) error {
	if v < 0 || v > maxV {
		return fmt.Errorf("xollama config: media.%s.defaults.%s %d must be between 0 and %d", kind, field, v, maxV)
	}
	return nil
}

func oneOf(kind, field, v string, set []string) error {
	if v != "" && !slices.Contains(set, v) {
		return fmt.Errorf("xollama config: unknown media.%s.%s %q (want one of %v)", kind, field, v, set)
	}
	return nil
}

func nonNegative(kind, field string, v *float64) error {
	if v != nil && *v < 0 {
		return fmt.Errorf("xollama config: media.%s.defaults.%s %v must not be negative", kind, field, *v)
	}
	return nil
}

func (i *ImageMedia) validate() error {
	if i.Model == "" {
		return fmt.Errorf("xollama config: media.image.model is required")
	}
	if err := oneOf("image", "edit", i.Edit, validImageEdit); err != nil {
		return err
	}
	d := i.Defaults
	if d == nil {
		d = &ImageDefaults{}
	}
	for _, f := range []struct {
		name string
		v, m int
	}{{"width", d.Width, maxMediaSide}, {"height", d.Height, maxMediaSide}, {"steps", d.Steps, maxMediaSteps}} {
		if err := positive("image", f.name, f.v, f.m); err != nil {
			return err
		}
	}
	if err := nonNegative("image", "cfg", d.CFG); err != nil {
		return err
	}
	if err := nonNegative("image", "flow_shift", d.FlowShift); err != nil {
		return err
	}
	if d.Strength != nil && (*d.Strength <= 0 || *d.Strength > 1) {
		return fmt.Errorf("xollama config: media.image.defaults.strength %v must be above 0 and at most 1", *d.Strength)
	}
	if err := oneOf("image", "defaults.output_format", d.OutputFormat, validImageFormats); err != nil {
		return err
	}
	return i.MediaEngine.validate("image", d.stated, imageDefaultFields)
}

func (d *ImageDefaults) stated(f string) bool {
	switch f {
	case "width":
		return d.Width > 0
	case "height":
		return d.Height > 0
	case "steps":
		return d.Steps > 0
	case "cfg":
		return d.CFG != nil
	case "sampler":
		return d.Sampler != ""
	case "scheduler":
		return d.Scheduler != ""
	case "flow_shift":
		return d.FlowShift != nil
	case "seed":
		return d.Seed != nil
	case "output_format":
		return d.OutputFormat != ""
	case "strength":
		return d.Strength != nil
	}
	return false
}

func (s *STTMedia) validate() error {
	if s.Model == "" {
		return fmt.Errorf("xollama config: media.stt.model is required")
	}
	if s.Threads < 0 {
		return fmt.Errorf("xollama config: media.stt.threads %d must not be negative", s.Threads)
	}
	d := s.Defaults
	if d == nil {
		d = &STTDefaults{}
	}
	if err := oneOf("stt", "defaults.response_format", d.ResponseFormat, validSTTFormats); err != nil {
		return err
	}
	if err := oneOf("stt", "defaults.task", d.Task, validSTTTasks); err != nil {
		return err
	}
	return s.MediaEngine.validate("stt", d.stated, sttDefaultFields)
}

func (d *STTDefaults) stated(f string) bool {
	switch f {
	case "language":
		return d.Language != ""
	case "response_format":
		return d.ResponseFormat != ""
	case "task":
		return d.Task != ""
	}
	return false
}

func (t *TTSMedia) validate() error {
	if t.Model == "" {
		return fmt.Errorf("xollama config: media.tts.model is required")
	}
	if err := oneOf("tts", "engine", t.Engine, validTTSEngines); err != nil {
		return err
	}
	// OuteTTS produces audio codes; the WavTokenizer turns them into sound.
	// The engine refuses to boot one without the other.
	if (t.Engine == "" || t.Engine == "outetts") && t.Vocoder == "" {
		return fmt.Errorf("xollama config: media.tts.vocoder is required for the outetts engine (its WavTokenizer)")
	}
	// The engine refuses these at boot (opencoti #679); refused here, they
	// never reach a launch.
	if len(t.Voices) > 0 && t.Engine == "audiocpp" {
		return fmt.Errorf("xollama config: media.tts.voices: the audiocpp engine serves only its model's built-in voices and takes no voice file")
	}
	for name, digest := range t.Voices {
		if slices.Contains(reservedVoiceNames, name) {
			return fmt.Errorf("xollama config: media.tts.voices.%s: %q is a built-in voice name of the engine; pick another", name, name)
		}
		if !voiceName.MatchString(name) {
			return fmt.Errorf("xollama config: media.tts.voices: %q is not a voice name (letters, digits, '_', '.', '-', at most 64)", name)
		}
		if digest == "" {
			return fmt.Errorf("xollama config: media.tts.voices.%s has no file", name)
		}
	}
	names := make([]string, 0, len(t.VoiceMap))
	for k := range t.VoiceMap {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(t.VoiceMap[k]) == "" {
			return fmt.Errorf("xollama config: media.tts.voice_map maps %q to %q; both names are needed", k, t.VoiceMap[k])
		}
	}
	d := t.Defaults
	if d == nil {
		d = &TTSDefaults{}
	}
	if err := oneOf("tts", "defaults.response_format", d.ResponseFormat, validTTSFormats); err != nil {
		return err
	}
	if d.Speed != nil && (*d.Speed < minTTSSpeed || *d.Speed > maxTTSS) {
		return fmt.Errorf("xollama config: media.tts.defaults.speed %v must be between %v and %v", *d.Speed, minTTSSpeed, maxTTSS)
	}
	return t.MediaEngine.validate("tts", d.stated, ttsDefaultFields)
}

func (d *TTSDefaults) stated(f string) bool {
	switch f {
	case "voice":
		return d.Voice != ""
	case "language":
		return d.Language != ""
	case "response_format":
		return d.ResponseFormat != ""
	case "speed":
		return d.Speed != nil
	}
	return false
}

func (v *VideoMedia) validate() error {
	if v.Model == "" {
		return fmt.Errorf("xollama config: media.video.model is required")
	}
	d := v.Defaults
	if d == nil {
		d = &VideoDefaults{}
	}
	for _, f := range []struct {
		name string
		v, m int
	}{
		{"width", d.Width, maxMediaSide},
		{"height", d.Height, maxMediaSide},
		{"frames", d.Frames, maxVideoFrames},
		{"fps", d.FPS, maxVideoFPS},
		{"steps", d.Steps, maxMediaSteps},
	} {
		if err := positive("video", f.name, f.v, f.m); err != nil {
			return err
		}
	}
	if err := nonNegative("video", "cfg", d.CFG); err != nil {
		return err
	}
	if err := nonNegative("video", "flow_shift", d.FlowShift); err != nil {
		return err
	}
	if err := oneOf("video", "defaults.output_format", d.OutputFormat, validVideoFormats); err != nil {
		return err
	}
	return v.MediaEngine.validate("video", d.stated, videoDefaultFields)
}

func (d *VideoDefaults) stated(f string) bool {
	switch f {
	case "width":
		return d.Width > 0
	case "height":
		return d.Height > 0
	case "frames":
		return d.Frames > 0
	case "fps":
		return d.FPS > 0
	case "steps":
		return d.Steps > 0
	case "cfg":
		return d.CFG != nil
	case "sampler":
		return d.Sampler != ""
	case "flow_shift":
		return d.FlowShift != nil
	case "seed":
		return d.Seed != nil
	case "output_format":
		return d.OutputFormat != ""
	}
	return false
}

// Clone returns a deep copy, so an edit to the copy cannot reach the
// original's slices, maps or defaults.
func (m *Media) Clone() *Media {
	if m == nil {
		return nil
	}
	out := &Media{}
	if m.Image != nil {
		i := *m.Image
		i.MediaEngine = m.Image.MediaEngine.clone()
		if m.Image.Defaults != nil {
			d := *m.Image.Defaults
			i.Defaults = &d
		}
		out.Image = &i
	}
	if m.STT != nil {
		s := *m.STT
		s.MediaEngine = m.STT.MediaEngine.clone()
		if m.STT.Defaults != nil {
			d := *m.STT.Defaults
			s.Defaults = &d
		}
		out.STT = &s
	}
	if m.TTS != nil {
		t := *m.TTS
		t.MediaEngine = m.TTS.MediaEngine.clone()
		if m.TTS.Voices != nil {
			t.Voices = make(map[string]string, len(m.TTS.Voices))
			for k, v := range m.TTS.Voices {
				t.Voices[k] = v
			}
		}
		if m.TTS.VoiceMap != nil {
			t.VoiceMap = make(map[string]string, len(m.TTS.VoiceMap))
			for k, v := range m.TTS.VoiceMap {
				t.VoiceMap[k] = v
			}
		}
		if m.TTS.Defaults != nil {
			d := *m.TTS.Defaults
			t.Defaults = &d
		}
		out.TTS = &t
	}
	if m.Video != nil {
		v := *m.Video
		v.MediaEngine = m.Video.MediaEngine.clone()
		if m.Video.Defaults != nil {
			d := *m.Video.Defaults
			v.Defaults = &d
		}
		out.Video = &v
	}
	return out
}

func (e MediaEngine) clone() MediaEngine {
	e.Args = slices.Clone(e.Args)
	e.Fixed = slices.Clone(e.Fixed)
	return e
}

func (e *MediaEngine) isZero() bool {
	return e.Device == "" && e.ReserveMiB == 0 && len(e.Args) == 0 && len(e.Fixed) == 0
}

// Prune drops what states nothing: empty defaults, a kind with no field set,
// and the whole block when no kind is left. It returns the result, nil when
// nothing is left.
func (m *Media) Prune() *Media {
	if m == nil {
		return nil
	}
	if i := m.Image; i != nil {
		if i.Defaults != nil && *i.Defaults == (ImageDefaults{}) {
			i.Defaults = nil
		}
		if i.Model == "" && i.VAE == "" && i.LLM == "" && i.LLMVision == "" && i.Edit == "" && i.MediaEngine.isZero() && i.Defaults == nil {
			m.Image = nil
		}
	}
	if s := m.STT; s != nil {
		if s.Defaults != nil && *s.Defaults == (STTDefaults{}) {
			s.Defaults = nil
		}
		if s.Model == "" && s.Threads == 0 && s.MediaEngine.isZero() && s.Defaults == nil {
			m.STT = nil
		}
	}
	if t := m.TTS; t != nil {
		if t.Defaults != nil && *t.Defaults == (TTSDefaults{}) {
			t.Defaults = nil
		}
		if len(t.VoiceMap) == 0 {
			t.VoiceMap = nil
		}
		if len(t.Voices) == 0 {
			t.Voices = nil
		}
		if t.Engine == "" && t.Model == "" && t.Vocoder == "" && t.Voices == nil && t.VoiceMap == nil && t.MediaEngine.isZero() && t.Defaults == nil {
			m.TTS = nil
		}
	}
	if v := m.Video; v != nil {
		if v.Defaults != nil && *v.Defaults == (VideoDefaults{}) {
			v.Defaults = nil
		}
		if v.Model == "" && v.VAE == "" && v.TextEncoder == "" && v.MediaEngine.isZero() && v.Defaults == nil {
			m.Video = nil
		}
	}
	if m.IsZero() {
		return nil
	}
	return m
}

// MapComponents replaces every component reference with f's answer, in
// place: a catalog template's hf.co references become digests this way.
func (m *Media) MapComponents(f func(string) string) {
	if m.IsZero() {
		return
	}
	set := func(p *string) {
		if *p != "" {
			*p = f(*p)
		}
	}
	if i := m.Image; i != nil {
		set(&i.Model)
		set(&i.VAE)
		set(&i.LLM)
		set(&i.LLMVision)
	}
	if s := m.STT; s != nil {
		set(&s.Model)
	}
	if t := m.TTS; t != nil {
		set(&t.Model)
		set(&t.Vocoder)
		for name, digest := range t.Voices {
			t.Voices[name] = f(digest)
		}
	}
	if v := m.Video; v != nil {
		set(&v.Model)
		set(&v.VAE)
		set(&v.TextEncoder)
	}
}
