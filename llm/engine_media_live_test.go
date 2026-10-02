package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/xollama"
)

// TestMediaRunnerLive boots a real media-only engine with OuteTTS and
// Whisper in one process, speaks a sentence and transcribes it back.
//
// XOLLAMA_MEDIA_LIVE_DIR is the directory holding the files, laid out as
// <owner>/<repo>/<file>: a mirror filled by xollama media fetch (on solidPC
// /shared/dev/opencoti/.opencoti/models/media); the engine
// is the one XOLLAMA_ENGINE / XOLLAMA_ENGINE_PATH select.
func TestMediaRunnerLive(t *testing.T) {
	dir := os.Getenv("XOLLAMA_MEDIA_LIVE_DIR")
	if dir == "" {
		t.Skip("set XOLLAMA_MEDIA_LIVE_DIR to boot a real media engine")
	}
	files := map[string]string{
		"tts":     "OuteAI/OuteTTS-0.3-500M-GGUF/OuteTTS-0.3-500M-Q8_0.gguf",
		"vocoder": "ggml-org/WavTokenizer/WavTokenizer-Large-75-F16.gguf",
		"stt":     "ggerganov/whisper.cpp/ggml-large-v3-turbo-q8_0.bin",
	}
	path := func(d string) string { return filepath.Join(dir, files[d]) }
	size := func(d string) int64 {
		fi, err := os.Stat(path(d))
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	m := &xollama.Media{
		TTS: &xollama.TTSMedia{Model: "tts", Vocoder: "vocoder"},
		STT: &xollama.STTMedia{Model: "stt"},
	}
	r, err := NewMediaRunner("media:live", m, path, size, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := r.Load(ctx, ml.SystemInfo{}, nil, false); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := r.WaitUntilRunning(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("ready in %s, features %v", time.Since(start).Round(time.Millisecond), r.Features())

	sentence := "The quick brown fox jumps over the lazy dog."
	body, _ := json.Marshal(map[string]any{"model": "tts", "input": sentence, "response_format": "wav"})
	start = time.Now()
	resp, err := r.MediaDo(ctx, http.MethodPost, "v1/audio/speech", nil, bytes.NewReader(body), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	wav, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(wav, []byte("RIFF")) {
		t.Fatalf("speech = %d, %d bytes: %.200s", resp.StatusCode, len(wav), wav)
	}
	t.Logf("speech: %d bytes of %s in %s", len(wav), resp.Header.Get("Content-Type"), time.Since(start).Round(time.Millisecond))

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	mw.WriteField("model", "stt")
	fw, _ := mw.CreateFormFile("file", "fox.wav")
	fw.Write(wav)
	mw.Close()
	start = time.Now()
	resp, err = r.MediaDo(ctx, http.MethodPost, "v1/audio/transcriptions", nil, &form, mw.FormDataContentType())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Text string `json:"text"`
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(raw, &out)
	t.Logf("transcription in %s: %q", time.Since(start).Round(time.Millisecond), out.Text)
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(out.Text), "fox") {
		t.Fatalf("transcription = %d %s", resp.StatusCode, raw)
	}
}
