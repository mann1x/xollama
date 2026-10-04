package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/middleware"
	"github.com/ollama/ollama/types/xollama"
)

func TestMediaDefaultsReadEachKindOnceAndTheLastPairWins(t *testing.T) {
	t.Setenv("XOLLAMA_MEDIA_DEFAULTS", " image = a , image_edit=b, bogus=c, tts=, stt=whisper,nonsense")
	d := mediaDefaults()
	want := map[string]string{
		string(CapabilityImageGeneration): "a",
		string(CapabilityImageEdit):       "b",
		string(CapabilityTranscription):   "whisper",
	}
	if len(d) != len(want) {
		t.Fatalf("defaults = %v, want %v", d, want)
	}
	for k, v := range d {
		if strings.TrimSuffix(v.DisplayShortest(), ":latest") != want[string(k)] {
			t.Errorf("%s = %s, want %s", k, v.DisplayShortest(), want[string(k)])
		}
	}
}

func TestTheOperatorsDefaultIsMarkedAndServesARequestThatNamesNoModel(t *testing.T) {
	mediaStore(t)
	// speech=klein names a model without speech: not a default for it.
	t.Setenv("XOLLAMA_MEDIA_DEFAULTS", "image=klein,speech=klein,image_edit=zimage")
	s, e := mediaServer(t, map[string]*xollama.Media{
		"klein":  kleinMedia(t, ""),
		"zimage": kleinMedia(t, ""),
	})

	r := gin.New()
	r.GET("/v1/models", s.mediaModelsMiddleware(), middleware.ListMiddleware(), s.ListHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var l struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	marked := map[string][]string{}
	for _, m := range l.Data {
		id := strings.TrimSuffix(m["id"].(string), ":latest")
		if d, ok := m["default_for"]; ok {
			marked[id] = toStrings(d)
		}
	}
	if !slices.Equal(marked["klein"], []string{"image_generation"}) || !slices.Equal(marked["zimage"], []string{"image_edit"}) || len(marked) != 2 {
		t.Fatalf("default_for = %v", marked)
	}

	w = postJSON(t, s.ImageGenerationsHandler, `{"prompt":"a lighthouse"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("generate without a model = %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(e.last(t).body, &got); err != nil {
		t.Fatal(err)
	}
	if m, _ := got["model"].(string); strings.TrimSuffix(m, ":latest") != "klein" {
		t.Errorf("the engine was asked for model %v, want the default klein", got["model"])
	}

	// No default for speech: a request without a model is still refused.
	if w := postJSON(t, s.SpeechHandler, `{"input":"hi"}`); w.Code != http.StatusBadRequest {
		t.Errorf("speech without a model or a default = %d, want 400", w.Code)
	}
}

func TestWithoutDefaultsNothingIsMarkedAndAModelIsRequired(t *testing.T) {
	mediaStore(t)
	t.Setenv("XOLLAMA_MEDIA_DEFAULTS", "")
	s, _ := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, "")})
	if d := mediaDefaultFor("klein", mediaCapabilities(mustModel(t, "klein"))); d != nil {
		t.Errorf("default_for = %v with no defaults set", d)
	}
	if w := postJSON(t, s.ImageGenerationsHandler, `{"prompt":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("generate without a model = %d, want 400", w.Code)
	}
}

func mustModel(t *testing.T, name string) *Model {
	t.Helper()
	m, err := GetModel(name)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
