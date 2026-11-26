package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/types/xollama"
)

func TestWhisperWordTimestampValidation(t *testing.T) {
	mediaStore(t)
	// Only the model header is needed: validation must precede inference.
	t.Setenv("XOLLAMA_WHISPER_CLI", "/must-not-run-whisper")
	s, _ := mediaServer(t, map[string]*xollama.Media{"whisper": {STT: &xollama.STTMedia{Model: mediaDigest(t, []byte("lmggdummy"))}}})
	r := gin.New()
	r.POST("/v1/audio/transcriptions", s.mediaTranscriptionMiddleware())
	for _, tc := range []struct{ name, field, value string }{
		{"format", "response_format", "json"},
		{"temperature", "temperature", "NaN"},
		{"stream", "stream", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{"model": "whisper", "response_format": "verbose_json", "timestamp_granularities[]": "word"}
			fields[tc.field] = tc.value
			body, ct := multipartBody(t, fields, map[string][]string{"file": {"speech"}})
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
