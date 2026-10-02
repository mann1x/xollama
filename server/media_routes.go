package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/openai"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// The OpenAI media routes (plans/media-integration.md §4.4). Each request is
// filled from the model's template before it reaches the engine: a field the
// client left out gets the template's default, and a field the template lists
// in `fixed` gets the template's value whatever the client sent. The engine
// was booted with the same defaults (llm.MediaArgs), so the two agree; the
// fill-in makes the template hold even where an engine's own default differs.
//
// xollama never transcodes: what the engine answers is what the client gets,
// apart from `response_format: url`, which an image engine cannot serve and
// is answered as a data: URL.

const (
	// mediaQueueDepth is how many requests may wait for one engine before
	// the next is refused with 503. opencoti serves one request per engine
	// and answers a second with 503; xollama queues in front of it instead.
	mediaQueueDepth = 16
	// mediaQueueWait bounds the wait for a turn, and for an engine that is
	// still busy (its own 503) once the turn has come.
	mediaQueueWait = 10 * time.Minute
	// mediaFormMemory is how much of a multipart body is held in memory.
	mediaFormMemory = 64 << 20
)

var errMediaBusy = errors.New("the media engine is busy")

// mediaQueue is one FIFO turn per engine: a model's image, speech and
// transcription engines live in one process but each serves one request at
// a time, so each has its own queue.
type mediaQueue struct {
	mu    sync.Mutex
	slots map[string]*mediaSlot
}

type mediaSlot struct {
	turn    chan struct{}
	waiting int
}

var mediaQueues = &mediaQueue{slots: map[string]*mediaSlot{}}

// acquire waits for key's turn. It returns errMediaBusy at once when the
// queue is full, and when the wait runs out.
func (q *mediaQueue) acquire(ctx context.Context, key string) (func(), error) {
	q.mu.Lock()
	s := q.slots[key]
	if s == nil {
		s = &mediaSlot{turn: make(chan struct{}, 1)}
		q.slots[key] = s
	}
	if s.waiting >= mediaQueueDepth {
		q.mu.Unlock()
		return nil, errMediaBusy
	}
	s.waiting++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		s.waiting--
		q.mu.Unlock()
	}()

	timer := time.NewTimer(mediaQueueWait)
	defer timer.Stop()
	select {
	case s.turn <- struct{}{}:
		return func() { <-s.turn }, nil
	case <-timer.C:
		return nil, errMediaBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// retryAfter reads an engine's Retry-After, in seconds; one second when it
// names none.
func retryAfter(h http.Header) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Second
}

// mediaForward sends one request to the engine in its turn, and asks again
// after the engine's Retry-After while it answers 503.
func mediaForward(ctx context.Context, r llm.MediaRunner, path string, body []byte, contentType string) (*http.Response, error) {
	deadline := time.Now().Add(mediaQueueWait)
	for {
		resp, err := r.MediaDo(ctx, http.MethodPost, path, nil, bytes.NewReader(body), contentType)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			return resp, nil
		}
		wait := retryAfter(resp.Header)
		resp.Body.Close()
		if time.Now().Add(wait).After(deadline) {
			return nil, errMediaBusy
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func mediaError(c *gin.Context, code int, msg string) {
	if code == http.StatusServiceUnavailable {
		c.Header("Retry-After", "5")
	}
	c.AbortWithStatusJSON(code, openai.NewError(code, msg))
}

// mediaKindName is a capability as a sentence names it.
var mediaKindName = map[model.Capability]string{
	CapabilityImageGeneration: "image generation",
	CapabilityImageEdit:       "image edits",
	CapabilitySpeech:          "speech",
	CapabilityTranscription:   "transcription",
	CapabilityVideo:           "video",
}

// serveMedia runs one media request: schedule the model's media engine,
// wait for the engine's turn, send body, and pass the reply on. reply may
// rewrite a successful JSON reply; nil streams the engine's bytes through.
func (s *Server) serveMedia(c *gin.Context, name string, kind model.Capability, engine, path string, body []byte, contentType string, reply func([]byte) ([]byte, error)) {
	ctx := c.Request.Context()
	r, _, err := s.mediaRunner(ctx, name, kind, nil)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			mediaError(c, http.StatusNotFound, fmt.Sprintf("model %q not found", name))
		case errors.Is(err, errNoMedia):
			mediaError(c, http.StatusBadRequest, fmt.Sprintf("model %q does not support %s", name, mediaKindName[kind]))
		case errors.Is(err, context.Canceled):
		default:
			mediaError(c, http.StatusInternalServerError, err.Error())
		}
		return
	}

	release, err := mediaQueues.acquire(ctx, r.ModelPath()+"/"+engine)
	if err != nil {
		if errors.Is(err, errMediaBusy) {
			mediaError(c, http.StatusServiceUnavailable, fmt.Sprintf("the %s engine of %q is busy; try again later", engine, name))
		}
		return
	}
	defer release()

	resp, err := mediaForward(ctx, r, path, body, contentType)
	if err != nil {
		switch {
		case errors.Is(err, errMediaBusy):
			mediaError(c, http.StatusServiceUnavailable, fmt.Sprintf("the %s engine of %q is busy; try again later", engine, name))
		case errors.Is(err, context.Canceled):
		default:
			mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		}
		return
	}
	defer resp.Body.Close()

	if reply != nil && resp.StatusCode == http.StatusOK {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
			return
		}
		if data, err = reply(data); err != nil {
			mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
			return
		}
		c.Data(http.StatusOK, "application/json", data)
		return
	}

	for _, h := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			c.Header(h, v)
		}
	}
	c.Status(resp.StatusCode)
	// Streamed speech (pcm, sse) is flushed as it comes.
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				return
			}
			c.Writer.Flush()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("media reply cut short", "model", name, "error", err)
			}
			return
		}
	}
}

// mediaFields is a request's fields, JSON or multipart, as the fill-in sees
// them.
type mediaFields interface {
	has(key string) bool
	get(key string) string
	set(key string, v any)
	// extra is the request's sd_cpp_extra_args object: stable-diffusion.cpp's
	// own generation JSON, which the engine applies after the OpenAI fields.
	extra() map[string]any
	setExtra(map[string]any)
}

type jsonFields map[string]any

func (f jsonFields) has(k string) bool {
	v, ok := f[k]
	if !ok || v == nil {
		return false
	}
	s, isString := v.(string)
	return !isString || s != ""
}

func (f jsonFields) get(k string) string {
	switch v := f[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case map[string]any:
		if id, ok := v["id"].(string); ok {
			return id
		}
	}
	return ""
}

func (f jsonFields) set(k string, v any) { f[k] = v }

func (f jsonFields) extra() map[string]any {
	if m, ok := f["sd_cpp_extra_args"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (f jsonFields) setExtra(m map[string]any) { f["sd_cpp_extra_args"] = m }

type formFields map[string][]string

func (f formFields) has(k string) bool { return len(f[k]) > 0 && f[k][0] != "" }

func (f formFields) get(k string) string {
	if len(f[k]) == 0 {
		return ""
	}
	return f[k][0]
}

func (f formFields) set(k string, v any) {
	switch v := v.(type) {
	case float64:
		f[k] = []string{strconv.FormatFloat(v, 'f', -1, 64)}
	default:
		f[k] = []string{fmt.Sprint(v)}
	}
}

func (f formFields) extra() map[string]any {
	m := map[string]any{}
	if s := f.get("sd_cpp_extra_args"); s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func (f formFields) setExtra(m map[string]any) {
	b, _ := json.Marshal(m)
	f["sd_cpp_extra_args"] = []string{string(b)}
}

// fill applies one template value: the template's when the field is fixed or
// the client left it out.
func fill(f mediaFields, fixed []string, field, key string, stated bool, v any) {
	if !stated {
		return
	}
	if slices.Contains(fixed, field) || !f.has(key) {
		f.set(key, v)
	}
}

// fillImage fills an image request from the template. edit is true for
// /v1/images/edits.
func fillImage(f mediaFields, i *xollama.ImageMedia, edit bool) {
	d := i.Defaults
	if d == nil {
		return
	}
	fx := i.Fixed
	// The size: the template's when the client names none. A fixed side
	// goes into sd_cpp_extra_args, which the engine applies after `size`,
	// so it holds against any size the client asks for.
	if (!f.has("size") || f.get("size") == "auto") && d.Width > 0 && d.Height > 0 {
		f.set("size", fmt.Sprintf("%dx%d", d.Width, d.Height))
	}
	fill(f, fx, "steps", "steps", d.Steps > 0, d.Steps)
	fill(f, fx, "cfg", "cfg_scale", d.CFG != nil, deref(d.CFG))
	fill(f, fx, "sampler", "sampler", d.Sampler != "", d.Sampler)
	fill(f, fx, "scheduler", "scheduler", d.Scheduler != "", d.Scheduler)
	fill(f, fx, "seed", "seed", d.Seed != nil, derefInt(d.Seed))
	fill(f, fx, "output_format", "output_format", d.OutputFormat != "", d.OutputFormat)
	// An explicit strength makes the engine edit as img2img, so it is only
	// filled where the template edits that way.
	if edit && i.Edit != "reference" {
		fill(f, fx, "strength", "strength", d.Strength != nil, deref(d.Strength))
	}

	x := f.extra()
	changed := false
	if slices.Contains(fx, "width") && d.Width > 0 {
		x["width"], changed = d.Width, true
	}
	if slices.Contains(fx, "height") && d.Height > 0 {
		x["height"], changed = d.Height, true
	}
	if slices.Contains(fx, "flow_shift") && d.FlowShift != nil {
		sp, _ := x["sample_params"].(map[string]any)
		if sp == nil {
			sp = map[string]any{}
		}
		sp["flow_shift"] = *d.FlowShift
		x["sample_params"], changed = sp, true
	}
	if changed {
		f.setExtra(x)
	}
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// fillSpeech fills a speech request from the template, then maps the voice:
// an OpenAI client asks for alloy or nova, and voice_map names the model's
// voice for it.
func fillSpeech(f jsonFields, t *xollama.TTSMedia) {
	if d := t.Defaults; d != nil {
		fx := t.Fixed
		fill(f, fx, "voice", "voice", d.Voice != "", d.Voice)
		fill(f, fx, "language", "language", d.Language != "", d.Language)
		fill(f, fx, "response_format", "response_format", d.ResponseFormat != "", d.ResponseFormat)
		fill(f, fx, "speed", "speed", d.Speed != nil, deref(d.Speed))
	}
	if mapped, ok := t.VoiceMap[f.get("voice")]; ok {
		if obj, isObj := f["voice"].(map[string]any); isObj {
			obj["id"] = mapped
		} else {
			f["voice"] = mapped
		}
	}
}

// fillTranscription fills a transcription request from the template and
// returns the engine path that serves it: the template's task decides for a
// request on /v1/audio/transcriptions, and a fixed task decides for both.
func fillTranscription(f formFields, s *xollama.STTMedia, translate bool) string {
	if d := s.Defaults; d != nil {
		fx := s.Fixed
		fill(f, fx, "language", "language", d.Language != "", d.Language)
		fill(f, fx, "response_format", "response_format", d.ResponseFormat != "", d.ResponseFormat)
		if d.Task != "" && (slices.Contains(fx, "task") || !translate) {
			translate = d.Task == "translate"
		}
	}
	if translate {
		return "/v1/audio/translations"
	}
	return "/v1/audio/transcriptions"
}

// readJSONBody reads a JSON object body with numbers kept as written.
func readJSONBody(c *gin.Context) (jsonFields, bool) {
	body := jsonFields{}
	dec := json.NewDecoder(c.Request.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		mediaError(c, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, false
	}
	return body, true
}

// mediaModel reads the model a media request names, for its template.
func mediaModel(c *gin.Context, name string) (*xollama.Media, bool) {
	if name == "" {
		mediaError(c, http.StatusBadRequest, "model is required")
		return nil, false
	}
	m, err := GetModel(name)
	if err != nil {
		mediaError(c, http.StatusNotFound, fmt.Sprintf("model %q not found", name))
		return nil, false
	}
	media := modelMedia(m)
	if media == nil {
		media = &xollama.Media{}
	}
	return media, true
}

// ImageGenerationsHandler serves POST /v1/images/generations.
func (s *Server) ImageGenerationsHandler(c *gin.Context) {
	body, ok := readJSONBody(c)
	if !ok {
		return
	}
	name := body.get("model")
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	if media.Image != nil {
		fillImage(body, media.Image, false)
	}
	asURL := body.get("response_format") == "url"
	delete(body, "response_format")
	data, err := json.Marshal(body)
	if err != nil {
		mediaError(c, http.StatusInternalServerError, err.Error())
		return
	}
	s.serveMedia(c, name, CapabilityImageGeneration, "image", "/v1/images/generations", data, "application/json", imageReply(asURL))
}

// ImageEditsHandler serves POST /v1/images/edits.
func (s *Server) ImageEditsHandler(c *gin.Context) {
	form, ok := readForm(c)
	if !ok {
		return
	}
	fields := formFields(form.Value)
	name := fields.get("model")
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	if media.Image != nil {
		fillImage(fields, media.Image, true)
	}
	asURL := fields.get("response_format") == "url"
	delete(fields, "response_format")
	data, ct, err := encodeForm(form)
	if err != nil {
		mediaError(c, http.StatusBadRequest, err.Error())
		return
	}
	s.serveMedia(c, name, CapabilityImageEdit, "image", "/v1/images/edits", data, ct, imageReply(asURL))
}

// imageReply answers response_format url as data: URLs; the engine answers
// b64_json only.
func imageReply(asURL bool) func([]byte) ([]byte, error) {
	if !asURL {
		return func(b []byte) ([]byte, error) { return b, nil }
	}
	return func(b []byte) ([]byte, error) {
		var r map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		format, _ := r["output_format"].(string)
		if format == "" {
			format = "png"
		}
		items, _ := r["data"].([]any)
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if b64, ok := m["b64_json"].(string); ok {
				if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
					return nil, fmt.Errorf("image is not base64: %w", err)
				}
				m["url"] = "data:image/" + format + ";base64," + b64
				delete(m, "b64_json")
			}
		}
		return json.Marshal(r)
	}
}

// SpeechHandler serves POST /v1/audio/speech.
func (s *Server) SpeechHandler(c *gin.Context) {
	body, ok := readJSONBody(c)
	if !ok {
		return
	}
	name := body.get("model")
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	if media.TTS != nil {
		fillSpeech(body, media.TTS)
	}
	data, err := json.Marshal(body)
	if err != nil {
		mediaError(c, http.StatusInternalServerError, err.Error())
		return
	}
	s.serveMedia(c, name, CapabilitySpeech, "tts", "/v1/audio/speech", data, "application/json", nil)
}

// TranslationsHandler serves POST /v1/audio/translations, which upstream
// does not have: only a model with a speech-to-text engine answers it.
func (s *Server) TranslationsHandler(c *gin.Context) {
	form, ok := readForm(c)
	if !ok {
		return
	}
	s.serveTranscription(c, form, true)
}

// mediaTranscriptionMiddleware sends /v1/audio/transcriptions to the model's
// speech-to-text engine when it has one. Every other model goes on to
// upstream's audio-LLM shim, which finds the form already parsed exactly as
// it would have parsed it (same call, same limit).
//
// xollama-hook: media (in front of middleware.TranscriptionMiddleware)
func (s *Server) mediaTranscriptionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := c.Request.ParseMultipartForm(25 << 20); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, openai.NewError(http.StatusBadRequest, "failed to parse multipart form: "+err.Error()))
			return
		}
		m, err := GetModel(c.Request.FormValue("model"))
		if err != nil || !slices.Contains(mediaCapabilities(m), CapabilityTranscription) {
			c.Next()
			return
		}
		s.serveTranscription(c, c.Request.MultipartForm, false)
		c.Abort()
	}
}

func (s *Server) serveTranscription(c *gin.Context, form *multipart.Form, translate bool) {
	fields := formFields(form.Value)
	name := fields.get("model")
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	path := "/v1/audio/transcriptions"
	if translate {
		path = "/v1/audio/translations"
	}
	if media.STT != nil {
		path = fillTranscription(fields, media.STT, translate)
	}
	data, ct, err := encodeForm(form)
	if err != nil {
		mediaError(c, http.StatusBadRequest, err.Error())
		return
	}
	s.serveMedia(c, name, CapabilityTranscription, "stt", path, data, ct, nil)
}

func readForm(c *gin.Context) (*multipart.Form, bool) {
	if err := c.Request.ParseMultipartForm(mediaFormMemory); err != nil {
		mediaError(c, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return nil, false
	}
	return c.Request.MultipartForm, true
}

// encodeForm writes a parsed form back as a multipart body, every file with
// its own part headers, so repeated image[] fields all reach the engine.
func encodeForm(form *multipart.Form) ([]byte, string, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, vs := range form.Value {
		for _, v := range vs {
			if err := w.WriteField(k, v); err != nil {
				return nil, "", err
			}
		}
	}
	for _, fhs := range form.File {
		for _, fh := range fhs {
			h := make(textproto.MIMEHeader, len(fh.Header))
			for k, v := range fh.Header {
				h[k] = slices.Clone(v)
			}
			part, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			f, err := fh.Open()
			if err != nil {
				return nil, "", err
			}
			_, err = io.Copy(part, f)
			f.Close()
			if err != nil {
				return nil, "", err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return b.Bytes(), w.FormDataContentType(), nil
}

// mediaModalities is what a model takes in and gives out, for /v1/models.
func mediaModalities(caps []model.Capability) (in, out []string) {
	add := func(s []string, v string) []string {
		if !slices.Contains(s, v) {
			s = append(s, v)
		}
		return s
	}
	for _, c := range caps {
		switch c {
		case model.CapabilityCompletion:
			in, out = add(in, "text"), add(out, "text")
		case model.CapabilityVision, CapabilityImageEdit:
			in = add(in, "image")
		case CapabilityImageGeneration:
			in, out = add(in, "text"), add(out, "image")
		case CapabilitySpeech:
			in, out = add(in, "text"), add(out, "audio")
		case CapabilityTranscription:
			in, out = add(in, "audio"), add(out, "text")
		case CapabilityVideo:
			in, out = add(in, "text"), add(out, "video")
		case model.CapabilityEmbedding:
			in, out = add(in, "text"), add(out, "embedding")
		}
	}
	return in, out
}

// hasMediaLayer reports whether the named model carries media, from its
// manifest alone.
func hasMediaLayer(name string) bool {
	mf, err := manifest.ParseNamedManifest(model.ParseName(name))
	if err != nil {
		return false
	}
	for _, l := range mf.Layers {
		if l.MediaType == xollama.MediaTypeMedia {
			return true
		}
	}
	return false
}

// bufferedWriter holds a handler's reply so it can be rewritten.
type bufferedWriter struct {
	gin.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *bufferedWriter) WriteHeader(code int)        { w.status = code }
func (w *bufferedWriter) WriteHeaderNow()             {}
func (w *bufferedWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }
func (w *bufferedWriter) WriteString(s string) (int, error) {
	return w.buf.WriteString(s)
}

func (w *bufferedWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// mediaModelsMiddleware adds input_modalities and output_modalities to the
// /v1/models entries of models with media, and filters the list by
// ?output_modalities= and ?input_modalities= (SurfSense desktop classifies
// models that way). With no model carrying media and no filter, the list
// goes out byte for byte as upstream wrote it.
//
// xollama-hook: media (in front of middleware.ListMiddleware)
func (s *Server) mediaModelsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		wantOut := splitModalities(c.Query("output_modalities"))
		wantIn := splitModalities(c.Query("input_modalities"))
		bw := &bufferedWriter{ResponseWriter: c.Writer}
		c.Writer = bw
		c.Next()
		c.Writer = bw.ResponseWriter

		out := bw.buf.Bytes()
		if bw.Status() == http.StatusOK {
			if rewritten, ok := rewriteModels(out, wantIn, wantOut); ok {
				out = rewritten
			}
		}
		c.Writer.WriteHeader(bw.Status())
		_, _ = c.Writer.Write(out)
	}
}

func splitModalities(q string) []string {
	var out []string
	for _, v := range strings.Split(q, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func rewriteModels(data []byte, wantIn, wantOut []string) ([]byte, bool) {
	var list struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, false
	}
	filter := len(wantIn) > 0 || len(wantOut) > 0
	changed := false
	kept := list.Data[:0]
	for _, e := range list.Data {
		id, _ := e["id"].(string)
		media := hasMediaLayer(id)
		if !media && !filter {
			kept = append(kept, e)
			continue
		}
		var caps []model.Capability
		if m, err := GetModel(id); err == nil {
			caps = m.Capabilities()
		}
		in, out := mediaModalities(caps)
		if media {
			e["input_modalities"], e["output_modalities"] = in, out
			changed = true
		}
		if filter && (!anyOf(in, wantIn) || !anyOf(out, wantOut)) {
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	if !changed {
		return nil, false
	}
	list.Data = kept
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(list); err != nil {
		return nil, false
	}
	return b.Bytes(), true
}

// anyOf reports whether have has one of want; an empty want matches all.
func anyOf(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}
