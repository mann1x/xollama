// Package whispertimestamps runs whisper.cpp's word alignment without changing
// the default opencoti transcription path.
package whispertimestamps

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Segment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Response struct {
	Task     string    `json:"task"`
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Words    []Word    `json:"words"`
	Segments []Segment `json:"segments,omitempty"`
}

type Options struct {
	CLI, Model, Language, Prompt, Temperature string
	Translate, Segments                       bool
}

type offsets struct{ From, To int64 }
type output struct {
	Model         struct{ Multilingual bool }
	Result        struct{ Language string }
	Transcription []struct {
		Text    string
		Offsets offsets
		Tokens  []struct {
			ID      int
			Offsets *offsets
		}
	}
}

// Convert uses whisper.cpp's audio-derived token intervals, preserving whole
// word text from --split-on-word segments (including UTF-8 and punctuation).
// Segment durations are never divided or interpolated into word timings.
func Convert(data []byte, duration float64, translate, segments bool) (*Response, error) {
	if duration < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return nil, fmt.Errorf("invalid audio duration")
	}
	var in output
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("invalid whisper.cpp JSON: %w", err)
	}
	out := &Response{Task: "transcribe", Language: in.Result.Language, Duration: duration, Words: []Word{}}
	if translate {
		out.Task = "translate"
	}
	eot := 50256
	if in.Model.Multilingual {
		eot++
	}
	var text strings.Builder
	for _, seg := range in.Transcription {
		word := strings.TrimSpace(seg.Text)
		if word == "" {
			continue
		}
		start, end := int64(-1), int64(-1)
		for _, token := range seg.Tokens {
			if token.ID >= eot {
				continue
			}
			if token.Offsets == nil || token.Offsets.From < 0 || token.Offsets.To < token.Offsets.From {
				return nil, fmt.Errorf("whisper.cpp returned a text token without alignment")
			}
			if start < 0 {
				start = token.Offsets.From
			}
			end = token.Offsets.To
		}
		if start < 0 || end < start {
			return nil, fmt.Errorf("whisper.cpp returned a word without alignment")
		}
		ws, we := float64(start)/1000, float64(end)/1000
		// Whisper pads its last analysis window. Drop words entirely in that
		// padding and bound the final real word by the actual recording.
		if ws >= duration {
			continue
		}
		we = math.Min(we, duration)
		out.Words = append(out.Words, Word{word, ws, we})
		text.WriteString(seg.Text)
		if segments {
			out.Segments = append(out.Segments, Segment{len(out.Segments), float64(seg.Offsets.From) / 1000, math.Min(float64(seg.Offsets.To)/1000, duration), seg.Text})
		}
	}
	out.Text = strings.TrimSpace(text.String())
	return out, nil
}

// Run gives every request a private directory and uses CommandContext so a
// cancelled request stops decoding and inference. No client string is a shell
// command, model path, or output path.
func Run(ctx context.Context, audio io.Reader, o Options) (*Response, error) {
	dir, err := os.MkdirTemp("", "xollama-whisper-words-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	f, err := os.Create(input)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(f, audio)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	wav := filepath.Join(dir, "audio.wav")
	// A seekable WAV file has a real data length, unlike ffmpeg's pipe header.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-protocol_whitelist", "file,pipe",
		"-format_whitelist", "wav,mp3,flac,ogg,mov,mp4,m4a,matroska,webm,aac,asf,aiff",
		"-i", input, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("audio decoding failed (ffmpeg): %w", err)
	}
	duration, err := wavDuration(wav)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(dir, "result")
	language := o.Language
	if language == "" {
		language = "auto"
	}
	args := []string{"-m", o.Model, "-f", wav, "-ojf", "-of", base, "-sow", "-ml", "1", "-l", language, "-t", "4", "-ng", "-np"}
	if o.Prompt != "" {
		args = append(args, "--prompt", o.Prompt)
	}
	if o.Temperature != "" {
		args = append(args, "-tp", o.Temperature)
	}
	if o.Translate {
		args = append(args, "-tr")
	}
	if err := exec.CommandContext(ctx, o.CLI, args...).Run(); err != nil {
		return nil, fmt.Errorf("whisper.cpp word alignment failed: %w", err)
	}
	data, err := os.ReadFile(base + ".json")
	if err != nil {
		return nil, err
	}
	return Convert(data, duration, o.Translate, o.Segments)
}

func wavDuration(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var header [12]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return 0, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return 0, fmt.Errorf("decoder did not produce WAV")
	}
	for {
		var chunk [8]byte
		if _, err := io.ReadFull(f, chunk[:]); err != nil {
			return 0, err
		}
		n := int64(binary.LittleEndian.Uint32(chunk[4:]))
		if string(chunk[:4]) == "data" {
			return float64(n) / 32000, nil
		}
		if _, err := f.Seek(n+n%2, io.SeekCurrent); err != nil {
			return 0, err
		}
	}
}
