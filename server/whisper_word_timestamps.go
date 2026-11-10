package server

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/internal/whispertimestamps"
	"github.com/ollama/ollama/types/xollama"
)

func whisperTimestampCLI() string {
	if path := os.Getenv("XOLLAMA_WHISPER_CLI"); path != "" {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(exe), "whisper-cli")
		if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
			return path
		}
	}
	return ""
}

// Opt-in companion backend for legacy whisper.cpp models. Requests which do
// not request words, other model formats and installations without the
// companion keep using the scheduled media engine.
func serveWhisperWordTimestamps(c *gin.Context, form *multipart.Form, media *xollama.Media, name string, translate bool) bool {
	if media.STT == nil || !slices.Contains(form.Value["timestamp_granularities[]"], "word") {
		return false
	}
	cli := whisperTimestampCLI()
	if cli == "" {
		return false
	}
	model := blobPath(media.STT.Model)
	f, err := os.Open(model)
	if err != nil {
		mediaError(c, http.StatusBadRequest, "Whisper model is missing from the store")
		return true
	}
	var magic [4]byte
	_, err = io.ReadFull(f, magic[:])
	f.Close()
	if err != nil || string(magic[:]) != "lmgg" {
		return false
	}
	fields := formFields(form.Value)
	if fields.get("response_format") != "verbose_json" {
		mediaError(c, http.StatusBadRequest, "word timestamps require response_format=verbose_json")
		return true
	}
	for _, grain := range form.Value["timestamp_granularities[]"] {
		if grain != "word" && grain != "segment" {
			mediaError(c, http.StatusBadRequest, "timestamp granularities must be word or segment")
			return true
		}
	}
	if temperature := fields.get("temperature"); temperature != "" {
		v, err := strconv.ParseFloat(temperature, 64)
		if err != nil || !(v >= 0 && v <= 1) {
			mediaError(c, http.StatusBadRequest, "temperature must be between 0 and 1")
			return true
		}
	}
	if fields.get("stream") == "true" {
		mediaError(c, http.StatusBadRequest, "word timestamp transcription does not support streaming")
		return true
	}
	files := form.File["file"]
	if len(files) != 1 {
		mediaError(c, http.StatusBadRequest, "one audio file is required")
		return true
	}
	m, err := GetModel(name)
	if err == nil {
		err = loadRequires(m)
	}
	if err != nil {
		mediaError(c, http.StatusBadRequest, err.Error())
		return true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	release, err := mediaQueues.acquire(ctx, "whisper-word-timestamps")
	if err != nil {
		mediaError(c, http.StatusServiceUnavailable, "Whisper word timestamp queue is busy")
		return true
	}
	defer release()
	audio, err := files[0].Open()
	if err != nil {
		mediaError(c, http.StatusBadRequest, "could not read audio file")
		return true
	}
	defer audio.Close()
	result, err := whispertimestamps.Run(ctx, audio, whispertimestamps.Options{
		CLI: cli, Model: model, Language: fields.get("language"), Prompt: fields.get("prompt"), Temperature: fields.get("temperature"),
		Translate: translate, Segments: slices.Contains(form.Value["timestamp_granularities[]"], "segment"),
	})
	if err != nil {
		mediaError(c, http.StatusInternalServerError, err.Error())
		return true
	}
	c.AbortWithStatusJSON(http.StatusOK, result)
	return true
}
