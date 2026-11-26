package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/middleware"
	"github.com/ollama/ollama/types/xollama"
)

// This checks the HTTP proxy contract only. It does not establish that the
// pinned engine can align Whisper words; that requires the live engine test.
func TestTranscriptionTimestampGranularitiesReachEngine(t *testing.T) {
	for _, granularities := range [][]string{nil, {"segment"}, {"word"}, {"word", "segment"}} {
		name := "default"
		if len(granularities) > 0 {
			name = granularities[0]
			if len(granularities) > 1 {
				name += "+segment"
			}
		}
		t.Run(name, func(t *testing.T) {
			mediaStore(t)
			s, e := mediaServer(t, map[string]*xollama.Media{
				"whisper": {STT: &xollama.STTMedia{
					Model:    mediaDigest(t, []byte("whisper")),
					Defaults: &xollama.STTDefaults{ResponseFormat: "json"},
				}},
			})
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			fields := map[string][]string{"model": {"whisper"}, "response_format": {"verbose_json"}}
			if granularities != nil {
				fields["timestamp_granularities[]"] = granularities
			}
			for key, values := range fields {
				for _, value := range values {
					if err := mw.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			part, err := mw.CreateFormFile("file", "speech.wav")
			if err != nil {
				t.Fatal(err)
			}
			const audio = "RIFF\x00test audio"
			if _, err := io.WriteString(part, audio); err != nil {
				t.Fatal(err)
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			r := gin.New()
			r.POST("/v1/audio/transcriptions", s.mediaTranscriptionMiddleware(), middleware.TranscriptionMiddleware(), func(c *gin.Context) {
				t.Error("Whisper request reached the chat transcription shim")
				c.Status(http.StatusTeapot)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("transcription = %d: %s", w.Code, w.Body.String())
			}
			call := e.last(t)
			form := engineForm(t, call)
			if call.path != "/v1/audio/transcriptions" || !slices.Equal(form.Value["timestamp_granularities[]"], granularities) ||
				!slices.Equal(form.Value["response_format"], []string{"verbose_json"}) {
				t.Fatalf("engine got %s %v", call.path, form.Value)
			}
			file, err := form.File["file"][0].Open()
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			got, err := io.ReadAll(file)
			if err != nil || string(got) != audio {
				t.Fatalf("engine audio = %q, error %v", got, err)
			}
		})
	}
}
