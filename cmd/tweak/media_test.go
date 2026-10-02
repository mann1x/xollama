package tweak

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/types/xollama"
)

func mediaFile(t *testing.T, content string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flux-2-klein-4b-Q8_0.gguf")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}

// mediaServer records the order of the requests and what each blob POST
// carried. have lists digests the server already holds.
type mediaServer struct {
	mu      sync.Mutex
	calls   []string
	blobs   map[string][]byte
	pulled  []string
	show    *xollama.Config
	created api.CreateRequest
}

func newMediaServer(t *testing.T, have ...string) *mediaServer {
	t.Helper()
	ms := &mediaServer{blobs: map[string][]byte{}, show: &xollama.Config{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		defer ms.mu.Unlock()
		ms.calls = append(ms.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/show":
			json.NewEncoder(w).Encode(api.ShowResponse{Xollama: ms.show})
		case strings.HasPrefix(r.URL.Path, "/api/blobs/"):
			d := strings.TrimPrefix(r.URL.Path, "/api/blobs/")
			if r.Method == http.MethodHead {
				for _, h := range have {
					if h == d {
						return
					}
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ms.blobs[d], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/api/xollama/media/pull":
			var req api.MediaPullRequest
			json.NewDecoder(r.Body).Decode(&req)
			ms.pulled = append(ms.pulled, req.Source+" "+req.Digest)
			json.NewEncoder(w).Encode(api.ProgressResponse{Status: "success", Digest: req.Digest})
		case r.URL.Path == "/api/create":
			json.NewDecoder(r.Body).Decode(&ms.created)
			json.NewEncoder(w).Encode(api.ProgressResponse{Status: "success"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XOLLAMA_HOST", srv.URL)
	return ms
}

func execute(t *testing.T, args ...string) string {
	t.Helper()
	cmd := modelCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func TestAMediaFileIsUploadedBeforeTheCreateThatNamesIt(t *testing.T) {
	path, digest := mediaFile(t, "klein weights")
	ms := newMediaServer(t)

	execute(t, "m:test", "--image="+path, "--image-steps=4", "--image-width=1024", "--image-fixed=width", "--yes")

	if string(ms.blobs[digest]) != "klein weights" {
		t.Fatalf("blobs = %v, want the file under its digest", ms.blobs)
	}
	post, create := -1, -1
	for i, c := range ms.calls {
		switch c {
		case "POST /api/blobs/" + digest:
			post = i
		case "POST /api/create":
			create = i
		}
	}
	if post < 0 || create < post {
		t.Fatalf("calls = %v: the blob must reach the server before the create naming it", ms.calls)
	}
	img := ms.created.Xollama.Media.Image
	if img.Model != digest || img.Defaults.Steps != 4 || img.Defaults.Width != 1024 || img.Fixed[0] != "width" {
		t.Fatalf("created image media = %+v", img)
	}
}

func TestABlobTheServerHasIsNotSentAgain(t *testing.T) {
	path, digest := mediaFile(t, "kokoro")
	ms := newMediaServer(t, digest)

	out := execute(t, "m:test", "--tts="+path, "--tts-engine=audiocpp", "--yes")

	if len(ms.blobs) != 0 {
		t.Fatalf("uploaded %d blobs the server already had", len(ms.blobs))
	}
	if !strings.Contains(out, "media/tts.model is already on the server") {
		t.Fatalf("output does not say why nothing was sent:\n%s", out)
	}
}

func TestADryRunSendsNoMedia(t *testing.T) {
	path, _ := mediaFile(t, "whisper")
	ms := newMediaServer(t)

	execute(t, "m:test", "--stt="+path, "--dry-run")

	for _, c := range ms.calls {
		if strings.Contains(c, "/api/blobs/") || strings.Contains(c, "/api/create") {
			t.Fatalf("a dry run made %s", c)
		}
	}
}

func TestAMediaSettingWithoutItsModelIsDroppedWithItsReason(t *testing.T) {
	cfg, out, err := run(t, &xollama.Config{}, []string{"--image-steps=4"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Media != nil {
		t.Fatalf("media = %+v, want nothing: steps configure an engine that is not there", cfg.Media)
	}
	if !strings.Contains(out, "media.image.model is not set") {
		t.Fatalf("the drop is not explained:\n%s", out)
	}
}

func TestSetBlob(t *testing.T) {
	var dst string
	d := "sha256:" + strings.Repeat("A", 64)
	if err := setBlob(d, &dst); err != nil || dst != strings.ToLower(d) {
		t.Fatalf("a digest: dst = %q, err = %v", dst, err)
	}
	for in, want := range map[string]string{
		"sha256:abc":                   "not a sha256 digest",
		"hf.co/leejet/FLUX.2-klein-4B": "want hf.co/<owner>/<repo>/<file>",
		t.TempDir():                    "is a directory",
		"/no/such/file.gguf":           "no such file",
	} {
		if err := setBlob(in, &dst); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("setBlob(%q) err = %v, want %q", in, err, want)
		}
	}
	if err := setBlob("unset", &dst); err != nil || dst != "" {
		t.Fatalf("unset: dst = %q, err = %v", dst, err)
	}
}

func TestShowListsAModelsMedia(t *testing.T) {
	cfg := &xollama.Config{Media: &xollama.Media{TTS: &xollama.TTSMedia{
		Engine: "audiocpp", Model: "sha256:" + strings.Repeat("e", 64),
		VoiceMap: map[string]string{"nova": "af_bella", "alloy": "af_heart"},
		Defaults: &xollama.TTSDefaults{Voice: "af_heart"},
	}}}
	rows := map[string]string{}
	for _, r := range SettingRows(cfg) {
		rows[r[0]] = r[1]
	}
	if rows["media.tts.model"] == "" || rows["media.tts.defaults.voice"] != "af_heart" ||
		rows["media.tts.voice_map"] != "alloy=af_heart,nova=af_bella" || rows["media.tts.engine"] != "audiocpp" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestServerDefaultsLeaveMediaOut(t *testing.T) {
	for _, n := range serverFields() {
		for _, k := range []string{"image", "stt", "tts", "video"} {
			if n == k || strings.HasPrefix(n, k+"-") {
				t.Fatalf("tweak server offers %s: media belongs to a model, not to every model", n)
			}
		}
	}
}

// The walk edits a copy: the config read from the server is what the review
// compares against, so it must still say what the model stated.
func TestEditingMediaLeavesTheConfigItWasReadFromAlone(t *testing.T) {
	current := &xollama.Config{Media: &xollama.Media{Image: &xollama.ImageMedia{
		Model: "sha256:" + strings.Repeat("a", 64), Defaults: &xollama.ImageDefaults{Steps: 4},
	}}}
	cfg, _, err := run(t, current, []string{"--image-steps=8"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Media.Image.Defaults.Steps != 8 || current.Media.Image.Defaults.Steps != 4 {
		t.Fatalf("new steps = %d, current steps = %d; want 8 and 4", cfg.Media.Image.Defaults.Steps, current.Media.Image.Defaults.Steps)
	}
}

func TestAHuggingFaceComponentIsFetchedByTheServer(t *testing.T) {
	want := "sha256:" + strings.Repeat("5", 64)
	old := hubResolve
	t.Cleanup(func() { hubResolve = old })
	hubResolve = func(_ context.Context, r mediahub.Ref) (mediahub.File, error) {
		return mediahub.File{Ref: r, Digest: want}, nil
	}
	ms := newMediaServer(t)

	execute(t, "m:test", "--tts=hf.co/audio-cpp/audio.cpp-gguf/Kokoro-82M-GGUF/kokoro-82m-q8_0.gguf", "--tts-engine=audiocpp", "--yes")

	if len(ms.blobs) != 0 {
		t.Fatal("the CLI uploaded what the server should fetch")
	}
	if len(ms.pulled) != 1 || ms.pulled[0] != "hf.co/audio-cpp/audio.cpp-gguf/Kokoro-82M-GGUF/kokoro-82m-q8_0.gguf "+want {
		t.Fatalf("pulled = %v", ms.pulled)
	}
	if ms.created.Xollama.Media.TTS.Model != want {
		t.Fatalf("model = %q, want the hub's digest", ms.created.Xollama.Media.TTS.Model)
	}
}

func fakeHub(t *testing.T) {
	t.Helper()
	old := hubResolve
	t.Cleanup(func() { hubResolve = old })
	hubResolve = func(_ context.Context, r mediahub.Ref) (mediahub.File, error) {
		sum := sha256.Sum256([]byte(r.String()))
		return mediahub.File{Ref: r, Digest: fmt.Sprintf("sha256:%x", sum), Size: 1 << 20}, nil
	}
}

func mediaCreate(t *testing.T, args ...string) string {
	t.Helper()
	cmd := MediaCommand(Options{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"create"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func TestMediaCreateMakesATemplateWithNoWeights(t *testing.T) {
	fakeHub(t)
	ms := newMediaServer(t)

	mediaCreate(t, "media-kit", "kit:latest")

	if ms.created.Model != "kit:latest" || ms.created.From != "" {
		t.Fatalf("create = model %q from %q", ms.created.Model, ms.created.From)
	}
	m := ms.created.Xollama.Media
	if m.Image == nil || m.STT == nil || m.TTS == nil || !strings.HasPrefix(m.Image.Model, "sha256:") {
		t.Fatalf("media = %+v", m)
	}
	if m.Image.Defaults.Steps != 4 || m.TTS.VoiceMap["alloy"] != "af_alloy" {
		t.Fatal("the catalog's template settings did not reach the model")
	}
	// Five components, every one fetched by the server, none twice.
	if len(ms.pulled) != 5 {
		t.Fatalf("pulled %d: %v", len(ms.pulled), ms.pulled)
	}
}

func TestMediaCreateToAModelKeepsItsOtherSettings(t *testing.T) {
	fakeHub(t)
	ms := newMediaServer(t)
	ms.show = &xollama.Config{Version: 7, FlashAttention: "on", Media: &xollama.Media{
		STT: &xollama.STTMedia{Model: "sha256:" + strings.Repeat("1", 64)},
		TTS: &xollama.TTSMedia{Model: "sha256:" + strings.Repeat("2", 64), Vocoder: "sha256:" + strings.Repeat("3", 64)},
	}}

	mediaCreate(t, "kokoro-82m", "--to", "qwen3:8b")

	c := ms.created
	if c.Model != "qwen3:8b" || c.From != "qwen3:8b" {
		t.Fatalf("create = model %q from %q", c.Model, c.From)
	}
	if c.Xollama.FlashAttention != "on" || c.Xollama.Media.STT == nil {
		t.Fatalf("the model's other settings were lost: %+v", c.Xollama)
	}
	if c.Xollama.Media.TTS.Engine != "audiocpp" || c.Xollama.Media.TTS.Vocoder != "" {
		t.Fatalf("tts = %+v, want the catalog's replacing the old one whole", c.Xollama.Media.TTS)
	}
}

func TestMediaCreateDryRunFetchesNothing(t *testing.T) {
	fakeHub(t)
	ms := newMediaServer(t)
	out := mediaCreate(t, "flux2-klein-4b", "--dry-run")
	if len(ms.pulled) != 0 || ms.created.Model != "" {
		t.Fatalf("a dry run fetched %v or created %q", ms.pulled, ms.created.Model)
	}
	if !strings.Contains(out, `"version":7`) {
		t.Fatalf("dry run did not print the config:\n%s", out)
	}
}

func TestMediaListFiltersByKind(t *testing.T) {
	var out bytes.Buffer
	if err := printCatalog(&out, "tts"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kokoro-82m") || strings.Contains(out.String(), "flux2-klein-4b") {
		t.Fatalf("list --kind tts:\n%s", out.String())
	}
	if err := printCatalog(&out, "music"); err == nil {
		t.Fatal("an unknown kind listed something")
	}
}

func TestMediaCreateUploadsFromTheMirrorInsteadOfFetching(t *testing.T) {
	dir := t.TempDir()
	content := []byte("whisper weights")
	sum := sha256.Sum256(content)
	digest := fmt.Sprintf("sha256:%x", sum)
	old := hubResolve
	t.Cleanup(func() { hubResolve = old })
	hubResolve = func(_ context.Context, r mediahub.Ref) (mediahub.File, error) {
		return mediahub.File{Ref: r, Digest: digest, Size: int64(len(content))}, nil
	}
	e, _ := mediahub.Find("whisper-large-v3-turbo")
	ref, err := mediahub.ParseRef(e.Refs()[0])
	if err != nil {
		t.Fatal(err)
	}
	p := mediahub.MirrorPath(dir, ref)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ms := newMediaServer(t)

	out := mediaCreate(t, "whisper-large-v3-turbo", "whisper", "--dir", dir)

	if len(ms.pulled) != 0 {
		t.Fatalf("the server fetched %v although the mirror has it", ms.pulled)
	}
	if string(ms.blobs[digest]) != string(content) || !strings.Contains(out, "local copy") {
		t.Fatalf("blobs %v\n%s", ms.blobs, out)
	}
}

func TestMediaFetchKeepsWhatTheMirrorHas(t *testing.T) {
	fakeHub(t)
	var fetched []string
	old := mediaFetch
	t.Cleanup(func() { mediaFetch = old })
	mediaFetch = func(_ context.Context, dir string, f mediahub.File, _ func(int64, int64)) (string, bool, error) {
		fetched = append(fetched, f.Ref.String())
		return mediahub.MirrorPath(dir, f.Ref), len(fetched) == 1, nil
	}
	var out bytes.Buffer
	if err := runMediaFetch(t.Context(), &out, t.TempDir(), []string{"media-kit", "flux2-klein-4b"}); err != nil {
		t.Fatal(err)
	}
	// media-kit has five components and klein's three are among them.
	if len(fetched) != 5 {
		t.Fatalf("fetched %d: %v", len(fetched), fetched)
	}
	if !strings.Contains(out.String(), "fetched") || !strings.Contains(out.String(), "already there") {
		t.Fatal(out.String())
	}
}

func TestVoicesMarkTheDefaultAndTheNamesAClientMayUse(t *testing.T) {
	var out bytes.Buffer
	printVoices(&out, &api.MediaVoicesResponse{
		Default:         "af_heart",
		Voices:          []api.Voice{{ID: "af_bella"}, {ID: "af_heart", Aliases: []string{"nova", "shimmer"}}},
		ResponseFormats: []string{"mp3", "wav"}, SampleRate: 24000,
	})
	got := out.String()
	for _, want := range []string{"af_heart  nova, shimmer    *", "af_bella  -", "formats: mp3, wav at 24000 Hz"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}
