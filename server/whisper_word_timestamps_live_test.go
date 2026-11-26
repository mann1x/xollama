package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/internal/whispertimestamps"
	"github.com/ollama/ollama/types/xollama"
)

// This exercises the actual endpoint, model store, decoder and Whisper backend.
// Supply a speech WAV with at least two words; the ordinary tests need no model.
func TestWhisperWordTimestampsLive(t *testing.T) {
	cli, model, audio := os.Getenv("XOLLAMA_WHISPER_CLI"), os.Getenv("XOLLAMA_WHISPER_WORD_MODEL"), os.Getenv("XOLLAMA_WHISPER_WORD_AUDIO")
	if model == "" && audio == "" {
		t.Skip("set XOLLAMA_WHISPER_CLI, XOLLAMA_WHISPER_WORD_MODEL and XOLLAMA_WHISPER_WORD_AUDIO for real inference")
	}
	if cli == "" || model == "" || audio == "" {
		t.Fatal("CLI, model and audio are all required")
	}
	weights, err := os.ReadFile(model)
	if err != nil {
		t.Fatal(err)
	}
	wav, err := os.ReadFile(audio)
	if err != nil {
		t.Fatal(err)
	}
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"whisper": {STT: &xollama.STTMedia{Model: mediaDigest(t, weights)}}})
	r := gin.New()
	r.POST("/v1/audio/transcriptions", s.mediaTranscriptionMiddleware())
	for _, grain := range []string{"word", "segment"} {
		t.Run(grain, func(t *testing.T) {
			// The word case uses the real companion; segment-only must continue
			// using the original media runner.
			body, ct := multipartBody(t, map[string]string{"model": "whisper", "response_format": "verbose_json", "timestamp_granularities[]": grain}, map[string][]string{"file": {string(wav)}})
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("transcription = %d: %s", w.Code, w.Body.String())
			}
			if grain == "segment" {
				if e.last(t).path != "/v1/audio/transcriptions" {
					t.Fatal("segment request did not reach original engine")
				}
				return
			}
			var out whispertimestamps.Response
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Words) < 2 || out.Text == "" || out.Duration <= 0 {
				t.Fatalf("missing word alignment: %s", w.Body.String())
			}
			previous := 0.0
			for _, word := range out.Words {
				if word.Word == "" || word.Start < previous || word.End < word.Start || word.End > out.Duration {
					t.Fatalf("invalid word: %+v (duration %g)", word, out.Duration)
				}
				previous = word.End
			}
			t.Logf("%d words over %.3fs: %s; first word %+v", len(out.Words), out.Duration, out.Text, out.Words[0])
		})
	}
}
