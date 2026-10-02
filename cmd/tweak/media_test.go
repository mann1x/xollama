package tweak

import (
	"bytes"
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
	created api.CreateRequest
}

func newMediaServer(t *testing.T, have ...string) *mediaServer {
	t.Helper()
	ms := &mediaServer{blobs: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		defer ms.mu.Unlock()
		ms.calls = append(ms.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/api/show":
			json.NewEncoder(w).Encode(api.ShowResponse{})
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
		"hf.co/leejet/FLUX.2-klein-4B": "Hugging Face",
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
