package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/middleware"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// mediaCall is one request the fake engine received.
type mediaCall struct {
	path        string
	contentType string
	body        []byte
}

// fakeEngine is an opencoti media engine over HTTP: it records what it is
// sent and answers each route as the engine does.
type fakeEngine struct {
	mu    sync.Mutex
	calls []mediaCall
	busy  atomic.Int32 // answer 503 this many times first
	// videoStatus is what a poll of the engine's one video job answers.
	videoStatus atomic.Value
	srv         *httptest.Server
}

func newFakeEngine(t *testing.T) *fakeEngine {
	e := &fakeEngine{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.calls = append(e.calls, mediaCall{r.URL.Path, r.Header.Get("Content-Type"), body})
		e.mu.Unlock()
		if e.busy.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/images/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aGk="}],"output_format":"png"}`))
		case r.URL.Path == "/props":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"media":{"tts":{"voices":["af_heart","af_bella",{"id":"am_echo"}],"response_formats":["mp3","wav"],"sample_rate":24000}}}`))
		case r.URL.Path == "/v1/videos" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video_1","object":"video","status":"queued","progress":0}`))
		case r.URL.Path == "/v1/videos/video_1" && r.Method == http.MethodDelete:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video_1","object":"video.deleted","deleted":true}`))
		case r.URL.Path == "/v1/videos/video_1":
			status, _ := e.videoStatus.Load().(string)
			if status == "" {
				status = "in_progress"
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"video_1","object":"video","status":%q,"progress":50}`, status)
		case r.URL.Path == "/v1/videos/video_1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("MP4DATA"))
		case strings.HasPrefix(r.URL.Path, "/v1/videos/"):
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
		case r.URL.Path == "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write([]byte("RIFFwave"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"the quick brown fox"}`))
		}
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *fakeEngine) last(t *testing.T) mediaCall {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) == 0 {
		t.Fatal("the engine was not called")
	}
	return e.calls[len(e.calls)-1]
}

// engineMedia is a media runner whose engine is a fakeEngine.
type engineMedia struct {
	mockLlm
	url string
}

func (m *engineMedia) MediaDo(ctx context.Context, method, path string, _ url.Values, body io.Reader, ct string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.url+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ct)
	return http.DefaultClient.Do(req)
}

func (m *engineMedia) Features() []string { return nil }

// mediaServer is a server whose scheduler hands every media twin the fake
// engine, with the named models created in a fresh store.
func mediaServer(t *testing.T, models map[string]*xollama.Media) (*Server, *fakeEngine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// The components were written by mediaDigest before this call: a store
	// set only here would leave them in the real one.
	if os.Getenv("OLLAMA_MODELS") == "" {
		t.Fatal("call mediaStore before building the models")
	}
	e := newFakeEngine(t)
	s := &Server{sched: &Scheduler{
		pendingReqCh:    make(chan *LlmRequest, 1),
		finishedReqCh:   make(chan *LlmRequest, 1),
		expiredCh:       make(chan *runnerRef, 1),
		unloadedCh:      make(chan any, 1),
		loaded:          make(map[string]*runnerRef),
		getGpuFn:        getGpuFn,
		getSystemInfoFn: getSystemInfoFn,
		waitForRecovery: 250 * time.Millisecond,
		loadFn: func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
			if !isMediaKey(req.model.ModelPath) {
				t.Errorf("a media request scheduled %q, not the media twin", req.model.ModelPath)
			}
			req.successCh <- &runnerRef{llama: &engineMedia{mockLlm: mockLlm{modelPath: req.model.ModelPath}, url: e.srv.URL}}
			return false
		},
	}}
	go s.sched.Run(t.Context())
	for name, media := range models {
		w := createRequest(t, s.CreateHandler, api.CreateRequest{
			Model: name, Xollama: &xollama.Config{Media: media}, Stream: &noStream,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("create %s = %d %s", name, w.Code, w.Body.String())
		}
	}
	return s, e
}

// mediaStore points the test at a store of its own. It must run before
// mediaDigest, which writes the component blobs.
func mediaStore(t *testing.T) {
	t.Helper()
	t.Setenv("OLLAMA_MODELS", t.TempDir())
}

func postJSON(t *testing.T, h gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	return w
}

func multipartBody(t *testing.T, fields map[string]string, files map[string][]string) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for k, contents := range files {
		for i, content := range contents {
			fw, err := mw.CreateFormFile(k, k+string(rune('a'+i))+".bin")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fw.Write([]byte(content))
		}
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

// engineForm parses the multipart body the engine received.
func engineForm(t *testing.T, call mediaCall) *multipart.Form {
	t.Helper()
	_, params, err := mime.ParseMediaType(call.contentType)
	if err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(bytes.NewReader(call.body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	return form
}

func f64(v float64) *float64 { return &v }

func kleinMedia(t *testing.T, edit string, fixed ...string) *xollama.Media {
	return &xollama.Media{Image: &xollama.ImageMedia{
		Model: mediaDigest(t, []byte("klein")),
		Edit:  edit,
		Defaults: &xollama.ImageDefaults{
			Width: 1024, Height: 1024, Steps: 4, CFG: f64(1), Sampler: "euler",
			OutputFormat: "png", Strength: f64(0.6), FlowShift: f64(3),
		},
		MediaEngine: xollama.MediaEngine{Fixed: fixed},
	}}
}

func TestAnImageRequestIsFilledFromTheTemplate(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, "reference", "steps", "width", "flow_shift")})

	// A client that names nothing (SurfSense) gets the template's settings.
	w := postJSON(t, s.ImageGenerationsHandler, `{"model":"klein","prompt":"a lighthouse"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("generate = %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(e.last(t).body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"size": "1024x1024", "steps": 4.0, "cfg_scale": 1.0, "sampler": "euler", "output_format": "png"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["strength"]; ok {
		t.Error("a generation was sent a strength")
	}

	// A client's value wins, except where the template fixes the field.
	postJSON(t, s.ImageGenerationsHandler, `{"model":"klein","prompt":"x","size":"512x768","steps":30,"cfg_scale":7,"response_format":"b64_json"}`)
	got = nil
	_ = json.Unmarshal(e.last(t).body, &got)
	if got["size"] != "512x768" || got["cfg_scale"] != 7.0 {
		t.Errorf("the client's own values were overridden: %v", got)
	}
	if got["steps"] != 4.0 {
		t.Errorf("steps = %v, want the fixed 4", got["steps"])
	}
	extra, _ := got["sd_cpp_extra_args"].(map[string]any)
	sp, _ := extra["sample_params"].(map[string]any)
	if extra["width"] != 1024.0 || extra["height"] != nil || sp["flow_shift"] != 3.0 {
		t.Errorf("fixed width / flow_shift not applied after the size: %v", extra)
	}
	if _, ok := got["response_format"]; ok {
		t.Error("response_format reached the engine")
	}
}

func TestAnImageAskedForAsAURLIsADataURL(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, "reference")})
	w := postJSON(t, s.ImageGenerationsHandler, `{"model":"klein","prompt":"x","response_format":"url"}`)
	var r struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || len(r.Data) != 1 {
		t.Fatalf("reply %s: %v", w.Body.String(), err)
	}
	if r.Data[0]["url"] != "data:image/png;base64,aGk=" || r.Data[0]["b64_json"] != "" {
		t.Fatalf("data = %v", r.Data[0])
	}
}

func TestAnEditKeepsEveryImageAndFillsStrengthOnlyForImg2img(t *testing.T) {
	mediaStore(t)
	for _, tt := range []struct {
		edit     string
		strength bool
	}{{"reference", false}, {"img2img", true}} {
		t.Run(tt.edit, func(t *testing.T) {
			s, e := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, tt.edit)})
			body, ct := multipartBody(t, map[string]string{"model": "klein", "prompt": "change the sign"},
				map[string][]string{"image[]": {"one", "two"}})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", body)
			c.Request.Header.Set("Content-Type", ct)
			s.ImageEditsHandler(c)
			if w.Code != http.StatusOK {
				t.Fatalf("edit = %d %s", w.Code, w.Body.String())
			}
			call := e.last(t)
			if call.path != "/v1/images/edits" {
				t.Fatalf("path %s", call.path)
			}
			form := engineForm(t, call)
			if n := len(form.File["image[]"]); n != 2 {
				t.Fatalf("%d images reached the engine, want 2", n)
			}
			if got := len(form.Value["strength"]) > 0; got != tt.strength {
				t.Fatalf("strength sent = %v, want %v", got, tt.strength)
			}
			if form.Value["size"][0] != "1024x1024" || form.Value["steps"][0] != "4" {
				t.Fatalf("form = %v", form.Value)
			}
		})
	}
}

func TestSpeechMapsTheVoiceAndPassesTheAudioThrough(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"kokoro": {TTS: &xollama.TTSMedia{
		Engine: "audiocpp", Model: mediaDigest(t, []byte("kokoro")),
		VoiceMap: map[string]string{"alloy": "af_heart"},
		Defaults: &xollama.TTSDefaults{Voice: "af_bella", ResponseFormat: "mp3"},
	}}})

	w := postJSON(t, s.SpeechHandler, `{"model":"kokoro","input":"hello","voice":"alloy"}`)
	if w.Code != http.StatusOK || w.Body.String() != "RIFFwave" || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("speech = %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	var got map[string]any
	_ = json.Unmarshal(e.last(t).body, &got)
	if got["voice"] != "af_heart" || got["response_format"] != "mp3" {
		t.Fatalf("engine got %v", got)
	}

	postJSON(t, s.SpeechHandler, `{"model":"kokoro","input":"hello","response_format":"wav"}`)
	got = nil
	_ = json.Unmarshal(e.last(t).body, &got)
	if got["voice"] != "af_bella" || got["response_format"] != "wav" {
		t.Fatalf("engine got %v", got)
	}
}

func TestTranscriptionGoesToTheEngineOnlyForAModelWithSpeechToText(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{
		"whisper": {STT: &xollama.STTMedia{
			Model:    mediaDigest(t, []byte("whisper")),
			Defaults: &xollama.STTDefaults{Language: "en", ResponseFormat: "json"},
		}},
		"translator": {STT: &xollama.STTMedia{
			Model:    mediaDigest(t, []byte("whisper2")),
			Defaults: &xollama.STTDefaults{Task: "translate"},
		}},
	})
	upstream := 0
	r := gin.New()
	r.POST("/v1/audio/transcriptions", s.mediaTranscriptionMiddleware(), func(c *gin.Context) {
		upstream++
		if c.Request.FormValue("model") != "gemma3n" {
			t.Errorf("upstream's shim saw model %q", c.Request.FormValue("model"))
		}
		c.Status(http.StatusTeapot)
	})
	post := func(model string) *httptest.ResponseRecorder {
		body, ct := multipartBody(t, map[string]string{"model": model}, map[string][]string{"file": {"RIFF"}})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
		req.Header.Set("Content-Type", ct)
		r.ServeHTTP(w, req)
		return w
	}

	w := post("whisper")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "quick brown fox") || upstream != 0 {
		t.Fatalf("whisper = %d %s (upstream %d)", w.Code, w.Body.String(), upstream)
	}
	call := e.last(t)
	form := engineForm(t, call)
	if call.path != "/v1/audio/transcriptions" || form.Value["language"][0] != "en" || len(form.File["file"]) != 1 {
		t.Fatalf("engine got %s %v", call.path, form.Value)
	}

	post("translator")
	if p := e.last(t).path; p != "/v1/audio/translations" {
		t.Fatalf("a template whose task is translate was served at %s", p)
	}

	// A model without speech-to-text is upstream's, untouched.
	if w := post("gemma3n"); w.Code != http.StatusTeapot || upstream != 1 {
		t.Fatalf("gemma3n = %d (upstream %d)", w.Code, upstream)
	}
}

func TestMediaRequestsAreRefusedForAModelWithoutThem(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{"whisper": {STT: &xollama.STTMedia{Model: mediaDigest(t, []byte("w"))}}})
	if w := postJSON(t, s.ImageGenerationsHandler, `{"model":"whisper","prompt":"x"}`); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "does not support image generation") {
		t.Fatalf("image on whisper = %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, s.SpeechHandler, `{"model":"nope","input":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown model = %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, s.SpeechHandler, `{"input":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no model = %d", w.Code)
	}
}

func TestABusyEngineIsAskedAgainAfterItsRetryAfter(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, "reference")})
	e.busy.Store(1)
	start := time.Now()
	w := postJSON(t, s.ImageGenerationsHandler, `{"model":"klein","prompt":"x"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("generate = %d %s", w.Code, w.Body.String())
	}
	if len(e.calls) != 2 || time.Since(start) < time.Second {
		t.Fatalf("%d calls in %s, want a second call after Retry-After", len(e.calls), time.Since(start))
	}
}

func TestAFullQueueIsRefusedAtOnce(t *testing.T) {
	q := &mediaQueue{slots: map[string]*mediaSlot{}}
	release, err := q.acquire(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for range mediaQueueDepth {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := q.acquire(ctx, "k"); err == nil {
				r()
			}
		}()
	}
	for {
		q.mu.Lock()
		n := q.slots["k"].waiting
		q.mu.Unlock()
		if n == mediaQueueDepth {
			break
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	if _, err := q.acquire(t.Context(), "k"); err != errMediaBusy || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %s, want errMediaBusy at once", err, time.Since(start))
	}
	// Another engine has its own queue.
	if r, err := q.acquire(t.Context(), "other"); err != nil {
		t.Fatal(err)
	} else {
		r()
	}
	cancel()
	release()
	wg.Wait()
}

func TestModelsListCarriesModalitiesOnlyForMediaModels(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{
		"kokoro":  {TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: mediaDigest(t, []byte("k"))}},
		"whisper": {STT: &xollama.STTMedia{Model: mediaDigest(t, []byte("w"))}},
	})
	r := gin.New()
	r.GET("/v1/models", s.mediaModelsMiddleware(), middleware.ListMiddleware(), s.ListHandler)
	get := func(q string) map[string]map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models"+q, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("list = %d %s", w.Code, w.Body.String())
		}
		var l struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, e := range l.Data {
			out[strings.TrimSuffix(e["id"].(string), ":latest")] = e
		}
		return out
	}
	all := get("")
	if len(all) != 2 || !slices.Equal(toStrings(all["kokoro"]["output_modalities"]), []string{"audio"}) ||
		!slices.Equal(toStrings(all["whisper"]["input_modalities"]), []string{"audio"}) {
		t.Fatalf("list = %v", all)
	}
	if !slices.Equal(toStrings(all["kokoro"]["capabilities"]), []string{"speech"}) ||
		!slices.Equal(toStrings(all["whisper"]["capabilities"]), []string{"transcription"}) {
		t.Fatalf("capabilities: kokoro %v, whisper %v", all["kokoro"]["capabilities"], all["whisper"]["capabilities"])
	}
	if audio := get("?output_modalities=audio"); len(audio) != 1 || audio["kokoro"] == nil {
		t.Fatalf("output_modalities=audio = %v", audio)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestAListWithoutMediaIsUpstreamsByteForByte(t *testing.T) {
	data := []byte(`{"object":"list","data":[{"id":"llama3:latest","object":"model","created":1,"owned_by":"library"}]}` + "\n")
	if _, changed := rewriteModels(data, nil, nil); changed {
		t.Fatal("a list with no media model was rewritten")
	}
}

var _ llm.MediaRunner = (*engineMedia)(nil)

// TestTheRouterServesTheMediaRoutes goes through GenerateRoutes, so a hook
// line dropped from server/routes.go fails here.
func TestTheRouterServesTheMediaRoutes(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{
		"kokoro": {TTS: &xollama.TTSMedia{Engine: "audiocpp", Model: mediaDigest(t, []byte("k"))}},
		"klein":  kleinMedia(t, "reference"),
	})
	router, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	if w := do(http.MethodGet, "/v1/models?output_modalities=image", ""); !strings.Contains(w.Body.String(), `"klein:latest"`) ||
		strings.Contains(w.Body.String(), "kokoro") || !strings.Contains(w.Body.String(), "output_modalities") {
		t.Fatalf("/v1/models = %d %s", w.Code, w.Body.String())
	}
	for path, body := range map[string]string{
		"/v1/images/generations": `{"model":"klein","prompt":"x"}`,
		"/v1/audio/speech":       `{"model":"kokoro","input":"x"}`,
	} {
		if w := do(http.MethodPost, path, body); w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body.String())
		}
		if got := e.last(t).path; got != path {
			t.Fatalf("%s reached the engine as %s", path, got)
		}
	}
}

func TestVoicesMergeTheEnginesListWithTheTemplatesNames(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{
		"kokoro": {TTS: &xollama.TTSMedia{
			Engine: "audiocpp", Model: mediaDigest(t, []byte("k")),
			VoiceMap: map[string]string{"alloy": "af_alloy", "nova": "af_heart", "shimmer": "af_heart"},
			Defaults: &xollama.TTSDefaults{Voice: "af_heart"},
		}},
		"whisper": {STT: &xollama.STTMedia{Model: mediaDigest(t, []byte("w"))}},
	})
	router, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	get := func(req *http.Request) (*httptest.ResponseRecorder, api.MediaVoicesResponse) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var r api.MediaVoicesResponse
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		return w, r
	}

	w, r := get(httptest.NewRequest(http.MethodGet, api.XollamaMediaVoicesPath+"?model=kokoro", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("voices = %d %s", w.Code, w.Body.String())
	}
	want := []api.Voice{
		{ID: "af_alloy", Aliases: []string{"alloy"}}, // named by the template only
		{ID: "af_bella"},
		{ID: "af_heart", Aliases: []string{"nova", "shimmer"}},
		{ID: "am_echo"}, // an object in the engine's list
	}
	if fmt.Sprint(r.Voices) != fmt.Sprint(want) || r.Default != "af_heart" ||
		!slices.Equal(r.ResponseFormats, []string{"mp3", "wav"}) || r.SampleRate != 24000 {
		t.Fatalf("voices = %+v", r)
	}

	// The CLI's form: POST with a body.
	if w, r := get(httptest.NewRequest(http.MethodPost, api.XollamaMediaVoicesPath, strings.NewReader(`{"model":"kokoro"}`))); w.Code != http.StatusOK || len(r.Voices) != 4 {
		t.Fatalf("POST voices = %d %s", w.Code, w.Body.String())
	}
	if w, _ := get(httptest.NewRequest(http.MethodGet, api.XollamaMediaVoicesPath+"?model=whisper", nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("voices of a transcription model = %d %s", w.Code, w.Body.String())
	}
}
