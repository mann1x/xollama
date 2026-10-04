package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// A model's media engines (plans/media-integration.md) run in an opencoti
// process of their own, booted with no -m: the media-only mode. They are not
// added to the LLM's process because that combination -- media beside PolyKV
// pools, elastic slots and the rolling window -- is not yet gated by opencoti
// (#622, #623). The process is a runner like any other to the scheduler: it
// is placed, accounted, kept alive and evicted the same way.

// Engine features each media kind needs, as /health advertises them. The
// engine is the authority: a kind whose feature is missing is refused at load
// rather than started and answered with a 404 later.
const (
	FeatureImagesGenerate = "images_generate_v1"
	FeatureImagesEdit     = "images_edit_v1"
	FeatureTranscriptions = "audio_transcriptions_v1"
	FeatureSpeech         = "audio_speech_v1"
	FeatureVideos         = "videos_generate_v1"

	// FeatureSpeechContentFormat is an audio.cpp that tells a model file's
	// format by its contents, so it loads a blob path as it is (opencoti
	// bug-3880). Before it, audio.cpp read any path without ".gguf" as a
	// safetensors package and refused every published audio.cpp template.
	FeatureSpeechContentFormat = "audio_speech_content_format_v1"
	// FeatureSpeechVoiceFiles is --tts-voice NAME=PATH: one extra voice per
	// flag, each a file read by its contents from its blob path.
	FeatureSpeechVoiceFiles = "audio_speech_voice_files_v1"

	// The two below are advertised in /health only while the engine has the
	// sidecar loaded (opencoti #691; read off b117): the libraries the pin's
	// "#! sidecar" rows name and the build stages beside the engine. The
	// engine is asked, never the directory: a file that is there and did not
	// load serves nothing.

	// FeatureSpeechAudioCpp is an audio.cpp model loaded through its sidecar
	// (oc-audiocpp): Kokoro, Supertonic, KittenTTS.
	FeatureSpeechAudioCpp = "audio_speech_audiocpp_v1"
	// FeatureMediaCodec is the codec sidecar (oc-codec): mp3, opus and aac
	// speech, mp4 video.
	FeatureMediaCodec = "media_codec_v1"
)

// featureHints says what a missing feature means for the operator, for the
// features whose cause is not simply an older engine.
var featureHints = map[string]string{
	FeatureSpeechAudioCpp: "its audio.cpp sidecar (oc-audiocpp) is not loaded beside the engine",
	FeatureMediaCodec:     "its codec sidecar (oc-codec) is not loaded beside the engine, and this model's default format needs it",
}

// needsCodec reports whether the template's own default format is one the
// engine can only write with its codec sidecar. Without the sidecar the
// engine writes wav and pcm speech and avi and webm video, and refuses a
// request that states any other format with a 501 (measured on b117; opencoti
// #691), so a template that defaults to one of those is refused once, at
// load. A template that states no format leaves the choice to the engine,
// which then answers wav or avi, and needs nothing.
//
// The lists are the formats opencoti named as served WITHOUT the codec;
// everything else is taken to need it. flac speech and webp video were not
// named either way, so they are on the needing side.
func needsCodec(m *xollama.Media) bool {
	if t := m.TTS; t != nil && t.Defaults != nil {
		switch t.Defaults.ResponseFormat {
		case "", "wav", "pcm":
		default:
			return true
		}
	}
	if v := m.Video; v != nil && v.Defaults != nil {
		switch v.Defaults.OutputFormat {
		case "", "avi", "webm":
		default:
			return true
		}
	}
	return false
}

// Default reserves, the engine's own (handover 2026-09-26), used for the
// estimate when the template states none.
const (
	defaultImageReserveMiB = 3072
	defaultSTTReserveMiB   = 512
	defaultTTSReserveMiB   = 256
	defaultVideoReserveMiB = 3072
)

// MediaFeatures lists the engine features the media needs.
//
// Not gated yet: audio_speech_espeak_v1. The audio.cpp families that
// phonemise with eSpeak-ng (Kokoro, KittenTTS; not Supertonic) need it, but
// the engine does not advertise it yet and a template says only "audiocpp",
// not which family. Until the engine reports the family, it refuses such a
// boot itself, naming eSpeak-ng.
func MediaFeatures(m *xollama.Media) []string {
	if m.IsZero() {
		return nil
	}
	var out []string
	if m.Image != nil {
		out = append(out, FeatureImagesGenerate)
		if m.Image.Edit != "none" {
			out = append(out, FeatureImagesEdit)
		}
	}
	if m.STT != nil {
		out = append(out, FeatureTranscriptions)
	}
	if t := m.TTS; t != nil {
		out = append(out, FeatureSpeech)
		if t.Engine == "audiocpp" {
			out = append(out, FeatureSpeechContentFormat, FeatureSpeechAudioCpp)
		}
		if len(t.Voices) > 0 {
			out = append(out, FeatureSpeechVoiceFiles)
		}
	}
	if m.Video != nil {
		out = append(out, FeatureVideos)
	}
	if needsCodec(m) {
		out = append(out, FeatureMediaCodec)
	}
	return out
}

// sdOptions renders a kind's defaults as stable-diffusion.cpp options, the
// engine's per-request defaults (--diffusion-args), followed by the
// template's own sd options.
func sdOptions(d *xollama.ImageDefaults, extra []string) []string {
	var o []string
	if d != nil {
		o = appendSD(o, d.Width, d.Height, d.Steps, d.CFG, d.Sampler, d.Scheduler, d.FlowShift, d.Seed)
		if d.Strength != nil {
			o = append(o, "--strength", fmtFloat(*d.Strength))
		}
	}
	return append(o, extra...)
}

func videoOptions(d *xollama.VideoDefaults, extra []string) []string {
	var o []string
	if d != nil {
		o = appendSD(o, d.Width, d.Height, d.Steps, d.CFG, d.Sampler, "", d.FlowShift, d.Seed)
		if d.Frames > 0 {
			o = append(o, "--video-frames", strconv.Itoa(d.Frames))
		}
		if d.FPS > 0 {
			o = append(o, "--fps", strconv.Itoa(d.FPS))
		}
	}
	return append(o, extra...)
}

func appendSD(o []string, w, h, steps int, cfg *float64, sampler, scheduler string, shift *float64, seed *int64) []string {
	if w > 0 {
		o = append(o, "-W", strconv.Itoa(w))
	}
	if h > 0 {
		o = append(o, "-H", strconv.Itoa(h))
	}
	if steps > 0 {
		o = append(o, "--steps", strconv.Itoa(steps))
	}
	if cfg != nil {
		o = append(o, "--cfg-scale", fmtFloat(*cfg))
	}
	if sampler != "" {
		o = append(o, "--sampling-method", sampler)
	}
	if scheduler != "" {
		o = append(o, "--scheduler", scheduler)
	}
	if shift != nil {
		o = append(o, "--flow-shift", fmtFloat(*shift))
	}
	if seed != nil {
		o = append(o, "-s", strconv.FormatInt(*seed, 10))
	}
	return o
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func appendEngineCommon(args []string, flag string, e xollama.MediaEngine) []string {
	if e.Device != "" {
		args = append(args, "--"+flag+"-device", e.Device)
	}
	if e.ReserveMiB > 0 {
		args = append(args, "--"+flag+"-reserve-mib", strconv.Itoa(e.ReserveMiB))
	}
	return args
}

// MediaArgs is the engine command line for the media, after the server's own
// flags. path maps a component digest to its blob. Every component, each
// voice included, is passed as its blob path: the engine tells a file's
// format by its contents, never its name.
//
// The template's defaults reach the engine here, at boot, and again per
// request (server/media_routes.go), so they hold whatever the engine's own
// defaults are and whatever the client leaves out.
//
// The video flags and --tts-engine are opencoti M7's, assumed by the owner's
// word ahead of the build (2026-10-02); the M7 handoff confirms or renames
// them.
func MediaArgs(m *xollama.Media, path func(digest string) string) []string {
	if m.IsZero() {
		return nil
	}
	var a []string
	if i := m.Image; i != nil {
		a = append(a, "--diffusion-model", path(i.Model))
		if i.VAE != "" {
			a = append(a, "--diffusion-vae", path(i.VAE))
		}
		if i.LLM != "" {
			a = append(a, "--diffusion-llm", path(i.LLM))
		}
		if i.LLMVision != "" {
			a = append(a, "--diffusion-llm-vision", path(i.LLMVision))
		}
		switch i.Edit {
		case "reference":
			a = append(a, "--diffusion-edit", "ref")
		case "img2img":
			a = append(a, "--diffusion-edit", "img2img")
		}
		a = appendEngineCommon(a, "diffusion", i.MediaEngine)
		if o := sdOptions(i.Defaults, i.Args); len(o) > 0 {
			a = append(a, "--diffusion-args", strings.Join(o, " "))
		}
	}
	if s := m.STT; s != nil {
		a = append(a, "--stt-model", path(s.Model))
		a = appendEngineCommon(a, "stt", s.MediaEngine)
		if s.Threads > 0 {
			a = append(a, "--stt-threads", strconv.Itoa(s.Threads))
		}
		a = append(a, s.Args...)
	}
	if t := m.TTS; t != nil {
		if t.Engine != "" && t.Engine != "outetts" {
			a = append(a, "--tts-engine", t.Engine)
		}
		a = append(a, "--tts-model", path(t.Model))
		if t.Vocoder != "" {
			a = append(a, "--tts-vocoder", path(t.Vocoder))
		}
		names := make([]string, 0, len(t.Voices))
		for name := range t.Voices {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			a = append(a, "--tts-voice", name+"="+path(t.Voices[name]))
		}
		a = appendEngineCommon(a, "tts", t.MediaEngine)
		a = append(a, t.Args...)
	}
	if v := m.Video; v != nil {
		a = append(a, "--video-model", path(v.Model))
		if v.VAE != "" {
			a = append(a, "--video-vae", path(v.VAE))
		}
		if v.TextEncoder != "" {
			a = append(a, "--video-t5xxl", path(v.TextEncoder))
		}
		a = appendEngineCommon(a, "video", v.MediaEngine)
		if o := videoOptions(v.Defaults, v.Args); len(o) > 0 {
			a = append(a, "--video-args", strings.Join(o, " "))
		}
	}
	return a
}

// MediaEstimate is the memory the media's engines take: every component's
// size, plus each engine's working reserve. size maps a digest to its blob's
// size.
func MediaEstimate(m *xollama.Media, size func(digest string) int64) uint64 {
	if m.IsZero() {
		return 0
	}
	var total int64
	for _, c := range m.Components() {
		total += size(c.Digest)
	}
	reserve := func(stated, def int) int64 {
		if stated > 0 {
			return int64(stated) << 20
		}
		return int64(def) << 20
	}
	if m.Image != nil {
		total += reserve(m.Image.ReserveMiB, defaultImageReserveMiB)
	}
	if m.STT != nil {
		total += reserve(m.STT.ReserveMiB, defaultSTTReserveMiB)
	}
	if m.TTS != nil {
		total += reserve(m.TTS.ReserveMiB, defaultTTSReserveMiB)
	}
	if m.Video != nil {
		total += reserve(m.Video.ReserveMiB, defaultVideoReserveMiB)
	}
	return uint64(total)
}

// MediaRunner is a media-only engine process. It is a LlamaServer so the
// scheduler can own it; the text calls refuse.
type MediaRunner interface {
	LlamaServer
	// MediaDo sends one request to the engine's media surface. The caller
	// closes the body; status and body are the engine's own.
	MediaDo(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error)
	// Features is what the engine advertised once it was running.
	Features() []string
}

var errMediaOnly = errors.New("this runner serves media (images, speech, transcription), not text")

// NewMediaRunner prepares a media-only runner. Nothing starts until Load.
// key is the scheduler's key for it, returned as its ModelPath.
func NewMediaRunner(key string, m *xollama.Media, path func(string) string, size func(string) int64) (MediaRunner, error) {
	if m.IsZero() {
		return nil, errors.New("model has no media")
	}
	return &mediaRunner{
		key:      key,
		cpu:      mediaOnCPU(m),
		args:     MediaArgs(m, path),
		estimate: MediaEstimate(m, size),
		need:     MediaFeatures(m),
		done:     make(chan struct{}),
		client:   &http.Client{Timeout: 0},
	}, nil
}

type mediaRunner struct {
	key string
	// cpu is a template whose every engine states device CPU: it is never
	// placed on a GPU, and engine.Command, given no GPU, adds --gpu disable.
	cpu      bool
	args     []string
	estimate uint64
	need     []string

	mu       sync.Mutex
	cmd      *exec.Cmd
	port     int
	gpus     []ml.DeviceInfo
	onGPU    bool
	features []string
	done     chan struct{}
	exitErr  error
	tail     tailWriter

	client *http.Client
}

// mediaOnCPU reports whether every engine of m runs on the CPU: it states
// device CPU, or it is audio.cpp, which has no GPU backend in the engine. A
// speech model placed on a GPU would book memory it never uses and be launched
// with a --gpu backend the engine may not have (solidPC, b117, 2026-10-03: a
// Vulkan-only view of the host made every audio.cpp model fail to boot).
func mediaOnCPU(m *xollama.Media) bool {
	var devices []string
	if m.Image != nil {
		devices = append(devices, m.Image.Device)
	}
	if m.STT != nil {
		devices = append(devices, m.STT.Device)
	}
	if m.TTS != nil {
		if m.TTS.Engine == "audiocpp" {
			devices = append(devices, "cpu")
		} else {
			devices = append(devices, m.TTS.Device)
		}
	}
	if m.Video != nil {
		devices = append(devices, m.Video.Device)
	}
	for _, d := range devices {
		if !strings.EqualFold(d, "cpu") {
			return false
		}
	}
	return len(devices) > 0
}

// pickMediaGPU is the GPU with the most free memory that holds the estimate,
// or the one with the most free memory when none does. fits says which.
func pickMediaGPU(gpus []ml.DeviceInfo, need uint64) (ml.DeviceInfo, bool, bool) {
	var best ml.DeviceInfo
	found := false
	for _, g := range gpus {
		if !found || g.FreeMemory > best.FreeMemory {
			best, found = g, true
		}
	}
	if !found {
		return best, false, false
	}
	avail := uint64(0)
	if overhead := envconfig.GpuOverhead() + best.MinimumMemory(); best.FreeMemory > overhead {
		avail = best.FreeMemory - overhead
	}
	return best, true, need <= avail
}

func (r *mediaRunner) Load(ctx context.Context, _ ml.SystemInfo, gpus []ml.DeviceInfo, requireFull bool) ([]ml.DeviceID, error) {
	if r.cpu {
		gpus = nil
	}
	gpu, ok, fits := pickMediaGPU(gpus, r.estimate)
	if ok && !fits && requireFull {
		return nil, ErrLoadRequiredFull
	}
	var placed []ml.DeviceInfo
	if ok {
		placed = []ml.DeviceInfo{gpu}
	}
	if err := r.start(placed); err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []ml.DeviceID{gpu.DeviceID}, nil
}

func freePort() int {
	if a, err := net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		if l, err := net.ListenTCP("tcp", a); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port
		}
	}
	return rand.Intn(65535-49152) + 49152
}

func (r *mediaRunner) start(gpus []ml.DeviceInfo) error {
	// Media never runs on stock llama-server, so its binary is only the
	// fallback engine.Launch would hand back; a payload without one still
	// launches opencoti.
	exe, stockErr := FindLlamaServer()
	port := freePort()
	params := []string{"--port", strconv.Itoa(port), "--host", "127.0.0.1", "--no-webui", "--offline"}
	params = appendLlamaServerLogArgs(params)
	params = append(params, r.args...)

	name, args, opencoti := engine.Launch(exe, params, engineDevices(gpus), ml.LibOllamaPath)
	if !opencoti {
		if stockErr != nil {
			slog.Debug("no stock llama-server either", "error", stockErr)
		}
		return fmt.Errorf("media needs the opencoti engine, and it was not selected; check %s and that an artifact is installed", engine.EnvSelector)
	}
	libExe := exe
	if libExe == "" {
		libExe = name
	}

	// The GPU the scheduler placed it on is the only one it sees, so the
	// engine's default device -- the first GPU -- is that one.
	envs := ml.GetDevicesEnv(gpus)
	if envs == nil {
		envs = map[string]string{}
	}
	userHome, _ := os.UserHomeDir()
	payloadHome := engine.PayloadHome(engine.ArtifactOf(name, args), ml.LibOllamaPath, userHome)
	if payloadHome != "" {
		envs["HOME"] = payloadHome
	}
	if opencoti && engine.LegacyCUDA(engineDevices(gpus)) {
		envs[engine.EnvCUDALegacy] = "1"
	}

	cmd := exec.Command(name, args...)
	cmd.Stdout = &r.tail
	cmd.Stderr = &r.tail
	cmd.SysProcAttr = LlamaServerSysProcAttr
	SetupLlamaServerCommandEnv(cmd, libExe, ml.LibraryPaths(gpus), envs)
	slog.Info("starting media engine", "model", r.key, "estimate", format.HumanBytes2(r.estimate), "cmd", cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	bindEngineLifetime(cmd)

	r.mu.Lock()
	r.cmd, r.port, r.gpus, r.onGPU = cmd, port, gpus, len(gpus) > 0
	r.mu.Unlock()
	go func() {
		err := cmd.Wait()
		engine.AdoptPayloadHome(payloadHome)
		r.mu.Lock()
		r.exitErr = err
		r.mu.Unlock()
		close(r.done)
	}()
	return nil
}

func (r *mediaRunner) url(path string, q url.Values) string {
	u := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(r.GetPort())), Path: "/" + strings.TrimPrefix(path, "/")}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

type mediaHealth struct {
	Status   string   `json:"status"`
	Features []string `json:"features"`
}

func (r *mediaRunner) health(ctx context.Context) (mediaHealth, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url("health", nil), nil)
	if err != nil {
		return mediaHealth{}, 0, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return mediaHealth{}, 0, err
	}
	defer resp.Body.Close()
	var h mediaHealth
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
			// Read as "no features", an unreadable body would be blamed on
			// an engine that lacks them.
			return mediaHealth{}, resp.StatusCode, fmt.Errorf("media engine /health: unreadable body: %w", err)
		}
	}
	return h, resp.StatusCode, nil
}

// refusal names what the engine lacks, and why when the cause is known.
func refusal(missing []string) error {
	parts := make([]string, len(missing))
	for i, f := range missing {
		parts[i] = f
		if hint := featureHints[f]; hint != "" {
			parts[i] += " (" + hint + ")"
		}
	}
	return fmt.Errorf("the opencoti engine does not offer %s, which this model's media needs; update the engine", strings.Join(parts, ", "))
}

// missingFeatures is what the media needs and the engine does not offer.
func missingFeatures(need, have []string) []string {
	var out []string
	for _, f := range need {
		if !slices.Contains(have, f) {
			out = append(out, f)
		}
	}
	return out
}

func (r *mediaRunner) WaitUntilRunning(ctx context.Context) error {
	deadline := time.Now().Add(envconfig.LoadTimeout())
	for {
		select {
		case <-r.done:
			return fmt.Errorf("media engine exited while loading: %s", r.tail.last())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		h, code, err := r.health(hctx)
		cancel()
		if err != nil && code == http.StatusOK {
			// The engine is up and answered something that is not its
			// health: waiting longer will not change it.
			_ = r.Close()
			return err
		}
		if err == nil && code == http.StatusOK {
			if missing := missingFeatures(r.need, h.Features); len(missing) > 0 {
				_ = r.Close()
				return refusal(missing)
			}
			r.mu.Lock()
			r.features = h.Features
			r.mu.Unlock()
			slog.Info("media engine running", "model", r.key, "features", h.Features)
			return nil
		}
		if time.Now().After(deadline) {
			_ = r.Close()
			return fmt.Errorf("media engine did not become ready within %s: %s", envconfig.LoadTimeout(), r.tail.last())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (r *mediaRunner) MediaDo(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.url(path, query), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return r.client.Do(req)
}

func (r *mediaRunner) Features() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.features)
}

func (r *mediaRunner) Ping(ctx context.Context) error {
	if r.HasExited() {
		return errors.New("media engine has exited")
	}
	_, code, err := r.health(ctx)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("media engine health %d", code)
	}
	return nil
}

func (r *mediaRunner) Close() error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	default:
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-r.done
	return nil
}

func (r *mediaRunner) HasExited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *mediaRunner) ModelPath() string { return r.key }

func (r *mediaRunner) MemorySize() (total, vram uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.onGPU {
		return r.estimate, r.estimate
	}
	return r.estimate, 0
}

func (r *mediaRunner) VRAMByGPU(id ml.DeviceID) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.gpus {
		if g.DeviceID == id {
			return r.estimate
		}
	}
	return 0
}

func (r *mediaRunner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		return -1
	}
	return r.cmd.Process.Pid
}

func (r *mediaRunner) GetPort() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port
}

func (r *mediaRunner) GetDeviceInfos(context.Context) []ml.DeviceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.gpus)
}

func (r *mediaRunner) ContextLength() int { return 0 }

func (r *mediaRunner) Completion(context.Context, CompletionRequest, func(CompletionResponse)) error {
	return errMediaOnly
}

func (r *mediaRunner) Chat(context.Context, ChatRequest, func(ChatResponse)) error {
	return errMediaOnly
}

func (r *mediaRunner) ApplyChatTemplate(context.Context, ChatRequest) (string, error) {
	return "", errMediaOnly
}

func (r *mediaRunner) Embedding(context.Context, string) ([]float32, int, error) {
	return nil, 0, errMediaOnly
}

func (r *mediaRunner) Tokenize(context.Context, string) ([]int, error) { return nil, errMediaOnly }

func (r *mediaRunner) Detokenize(context.Context, []int) (string, error) { return "", errMediaOnly }

// tailWriter passes the engine's output to the server log and keeps its last
// lines, so a failed load can say why. One line is not enough: the engine
// states the cause first and its advice last.
type tailWriter struct {
	mu    sync.Mutex
	lines []string
}

// tailLines is how many of the engine's last lines a failed load reports.
const tailLines = 4

func (w *tailWriter) Write(p []byte) (int, error) {
	os.Stderr.Write(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, l := range bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n")) {
		if s := strings.TrimSpace(string(l)); s != "" {
			w.lines = append(w.lines, s)
			if len(w.lines) > tailLines {
				w.lines = w.lines[len(w.lines)-tailLines:]
			}
		}
	}
	return len(p), nil
}

func (w *tailWriter) last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lines) == 0 {
		return "no output"
	}
	return strings.Join(w.lines, " | ")
}
