package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/xollama"
)

// The OpenAI videos API (/v1/videos) on a model's video engine.
//
// A clip is a job: the create answers at once with an id, the client polls
// it, then fetches the content. The job outlives the request that made it,
// so xollama keeps a table of its own:
//   - an id of xollama's for each engine job, since two engines number their
//     jobs alike and a poll names no model;
//   - the engine that runs it, and that engine's scheduler reference, held
//     until the clip is fetched, deleted, or kept past the engine's TTL, so
//     the engine is never unloaded under a clip.

var (
	// videoPoll is how often a job nobody polls is looked at, so its engine
	// is let go when the clip is done even if the client went away.
	videoPoll = 5 * time.Second
	// videoKeep is how long a finished clip holds its engine: opencoti's
	// --media-job-ttl default, after which the engine has dropped it anyway.
	videoKeep = 10 * time.Minute
	// videoFetched is how long a clip holds its engine after its content was
	// fetched, for a client that asks again after a dropped connection.
	videoFetched = time.Minute
)

// videoHold makes the context that holds a job's engine: it outlives the
// request that made the job. A variable so a test can watch it.
var videoHold = func(request context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(request))
}

type videoJob struct {
	id, engineID, model string
	runner              llm.MediaRunner
	created             time.Time
	// poll, keep and fetched are videoPoll, videoKeep and videoFetched as
	// they were when the job was made.
	poll, keep, fetched time.Duration

	mu   sync.Mutex
	last map[string]any // the engine's latest answer, with xollama's id
	lost bool           // the engine exited under the job; last is its failed end

	release context.CancelFunc
	gone    chan struct{}
	once    sync.Once
	endAt   time.Time   // under mu
	timer   *time.Timer // under mu
}

type videoTable struct {
	mu   sync.Mutex
	jobs map[string]*videoJob
}

var videoJobs = &videoTable{jobs: map[string]*videoJob{}}

func (t *videoTable) get(id string) *videoJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.jobs[id]
}

// end lets the engine go and forgets the job.
func (j *videoJob) end() {
	j.once.Do(func() {
		videoJobs.mu.Lock()
		delete(videoJobs.jobs, j.id)
		videoJobs.mu.Unlock()
		j.release()
		close(j.gone)
	})
}

// lose ends a job whose engine exited under it: the job stays, failed, for the
// keep, so a client that polls learns what happened instead of a 502 and then
// a 404 (eleven2go and solidPC, 2026-10-05: an engine that crashed at the
// start of a clip left its client polling a job that was no longer there).
func (j *videoJob) lose() map[string]any {
	j.mu.Lock()
	if !j.lost {
		obj := maps.Clone(j.last)
		obj["status"] = "failed"
		obj["error"] = map[string]any{
			"code":    "engine_exited",
			"message": "the video engine exited while it was making this clip; the server log has its last output",
		}
		j.last, j.lost = obj, true
		slog.Warn("video job lost its engine", "id", j.id, "engine_id", j.engineID, "model", j.model)
	}
	obj := maps.Clone(j.last)
	j.mu.Unlock()
	j.release()
	j.endAfter(j.keep)
	return obj
}

func (j *videoJob) isLost() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lost
}

// endAfter ends the job after d, unless an earlier end is already set: a
// fetched clip lets its engine go sooner than a finished one nobody fetched.
func (j *videoJob) endAfter(d time.Duration) {
	at := time.Now().Add(d)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.timer != nil {
		if !at.Before(j.endAt) {
			return
		}
		j.timer.Stop()
	}
	j.endAt, j.timer = at, time.AfterFunc(d, j.end)
}

func videoDone(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

// refresh asks the engine for the job and keeps its answer. A job whose
// engine exited answers its failed end.
func (j *videoJob) refresh(ctx context.Context) (map[string]any, int, error) {
	if j.isLost() {
		return j.lose(), http.StatusOK, nil
	}
	resp, err := j.runner.MediaDo(ctx, http.MethodGet, "/v1/videos/"+j.engineID, nil, nil, "")
	if err != nil {
		if j.runner.HasExited() {
			return j.lose(), http.StatusOK, nil
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	var obj map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return nil, 0, err
	}
	obj["id"], obj["model"] = j.id, j.model
	j.mu.Lock()
	j.last = obj
	j.mu.Unlock()
	return obj, http.StatusOK, nil
}

// watch looks at a job until its clip is done, then holds the engine for
// videoKeep. A job the engine no longer has ends at once; one whose engine
// exited is kept, failed, for the keep.
func (j *videoJob) watch() {
	t := time.NewTicker(j.poll)
	defer t.Stop()
	for {
		select {
		case <-j.gone:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		obj, code, _ := j.refresh(ctx)
		cancel()
		switch {
		case code == http.StatusNotFound || code == http.StatusGone:
			j.end()
			return
		case j.isLost():
			return
		case obj != nil && videoDone(fmt.Sprint(obj["status"])):
			j.endAfter(j.keep)
			return
		}
	}
}

func newVideoID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "video_" + hex.EncodeToString(b)
}

// fillVideo fills a video request from the template and refuses a clip
// larger than the template's own: its width x height x frames is the shape
// its memory reserve was sized for.
func fillVideo(f mediaFields, v *xollama.VideoMedia) error {
	d := v.Defaults
	if d == nil {
		return nil
	}
	fx := v.Fixed
	sized := d.Width > 0 && d.Height > 0
	if sized && (!f.has("size") || slices.Contains(fx, "width") || slices.Contains(fx, "height")) {
		f.set("size", fmt.Sprintf("%dx%d", d.Width, d.Height))
	}
	if slices.Contains(fx, "frames") && d.Frames > 0 {
		f.set("frames", d.Frames)
	}
	fill(f, fx, "fps", "fps", d.FPS > 0, d.FPS)
	fill(f, fx, "steps", "steps", d.Steps > 0, d.Steps)
	fill(f, fx, "cfg", "cfg_scale", d.CFG != nil, deref(d.CFG))
	fill(f, fx, "sampler", "sampler", d.Sampler != "", d.Sampler)
	fill(f, fx, "seed", "seed", d.Seed != nil, derefInt(d.Seed))
	fill(f, fx, "output_format", "output_format", d.OutputFormat != "", d.OutputFormat)
	if slices.Contains(fx, "flow_shift") && d.FlowShift != nil {
		x := f.extra()
		sp, _ := x["sample_params"].(map[string]any)
		if sp == nil {
			sp = map[string]any{}
		}
		sp["flow_shift"] = *d.FlowShift
		x["sample_params"] = sp
		f.setExtra(x)
	}

	if !sized || d.Frames <= 0 {
		return nil
	}
	w, h, err := videoSize(f.get("size"))
	if err != nil {
		return err
	}
	frames, err := videoFrames(f, d)
	if err != nil {
		return err
	}
	if w*h*frames > d.Width*d.Height*d.Frames {
		return fmt.Errorf("a %dx%d clip of %d frames is larger than this model serves (%dx%d, %d frames, the shape its memory is reserved for); ask for a smaller size or fewer seconds",
			w, h, frames, d.Width, d.Height, d.Frames)
	}
	return nil
}

func videoSize(s string) (int, int, error) {
	ws, hs, ok := strings.Cut(strings.ToLower(s), "x")
	w, werr := strconv.Atoi(strings.TrimSpace(ws))
	h, herr := strconv.Atoi(strings.TrimSpace(hs))
	if !ok || werr != nil || herr != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("size %q: want WIDTHxHEIGHT, for example 832x480", s)
	}
	return w, h, nil
}

// videoFrames is the clip's frame count: frames as asked, else seconds at the
// clip's fps plus the first frame (Wan's 2 s at 16 fps is 33), else the
// template's.
func videoFrames(f mediaFields, d *xollama.VideoDefaults) (int, error) {
	if f.has("frames") {
		n, err := strconv.Atoi(f.get("frames"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("frames %q: want a positive whole number", f.get("frames"))
		}
		return n, nil
	}
	if f.has("seconds") {
		s, err := strconv.ParseFloat(f.get("seconds"), 64)
		if err != nil || s <= 0 {
			return 0, fmt.Errorf("seconds %q: want a positive number", f.get("seconds"))
		}
		fps := d.FPS
		if f.has("fps") {
			if n, err := strconv.Atoi(f.get("fps")); err == nil && n > 0 {
				fps = n
			}
		}
		if fps <= 0 {
			fps = 16
		}
		return int(math.Round(s*float64(fps))) + 1, nil
	}
	return d.Frames, nil
}

// VideoCreateHandler serves POST /v1/videos, JSON or multipart (a starting
// image as input_reference).
func (s *Server) VideoCreateHandler(c *gin.Context) {
	var (
		fields mediaFields
		form   *multipart.Form
		body   jsonFields
		ok     bool
	)
	if strings.HasPrefix(c.ContentType(), "multipart/") {
		if form, ok = readForm(c); !ok {
			return
		}
		fields = formFields(form.Value)
	} else {
		if body, ok = readJSONBody(c); !ok {
			return
		}
		fields = body
	}
	name := mediaName(fields.get("model"), CapabilityVideo)
	fields.set("model", name)
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	if media.Video != nil {
		if err := fillVideo(fields, media.Video); err != nil {
			mediaError(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	var (
		data []byte
		ct   = "application/json"
		err  error
	)
	if form != nil {
		data, ct, err = encodeForm(form)
	} else {
		data, err = json.Marshal(body)
	}
	if err != nil {
		mediaError(c, http.StatusBadRequest, err.Error())
		return
	}

	// The engine reference lives as long as the job, not the request; a
	// client that leaves while the engine loads still cancels the load.
	ctx := c.Request.Context()
	jobCtx, release := videoHold(ctx)
	stop := context.AfterFunc(ctx, release)
	r, _, err := s.mediaRunner(jobCtx, name, CapabilityVideo, nil)
	if !stop() {
		release()
		return
	}
	if err != nil {
		release()
		mediaRunnerError(c, err, name, CapabilityVideo)
		return
	}
	resp, err := r.MediaDo(ctx, http.MethodPost, "/v1/videos", nil, bytes.NewReader(data), ct)
	if err != nil {
		release()
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		release()
		passThrough(c, resp)
		return
	}
	var obj map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		release()
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	j := &videoJob{
		id: newVideoID(), engineID: fmt.Sprint(obj["id"]), model: name,
		runner: r, created: time.Now(),
		poll: videoPoll, keep: videoKeep, fetched: videoFetched, release: release, gone: make(chan struct{}),
	}
	obj["id"], obj["model"] = j.id, name
	j.last = obj
	videoJobs.mu.Lock()
	videoJobs.jobs[j.id] = j
	videoJobs.mu.Unlock()
	go j.watch()
	slog.Info("video job", "id", j.id, "engine_id", j.engineID, "model", name)
	c.JSON(http.StatusOK, obj)
}

func videoJobOr404(c *gin.Context) *videoJob {
	j := videoJobs.get(c.Param("id"))
	if j == nil {
		mediaError(c, http.StatusNotFound, fmt.Sprintf("video %q not found", c.Param("id")))
	}
	return j
}

// VideoGetHandler serves GET /v1/videos/{id}.
func (s *Server) VideoGetHandler(c *gin.Context) {
	j := videoJobOr404(c)
	if j == nil {
		return
	}
	obj, code, err := j.refresh(c.Request.Context())
	switch {
	case err != nil:
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
	case code != http.StatusOK:
		if code == http.StatusNotFound || code == http.StatusGone {
			j.end()
		}
		mediaError(c, code, fmt.Sprintf("video %q is no longer on the engine", j.id))
	default:
		if videoDone(fmt.Sprint(obj["status"])) {
			j.endAfter(j.keep)
		}
		c.JSON(http.StatusOK, obj)
	}
}

// VideoContentHandler serves GET /v1/videos/{id}/content: the clip, streamed
// as the engine sends it.
func (s *Server) VideoContentHandler(c *gin.Context) {
	j := videoJobOr404(c)
	if j == nil {
		return
	}
	if j.isLost() {
		mediaError(c, http.StatusConflict, fmt.Sprintf("video %q failed: its engine exited, there is no clip", j.id))
		return
	}
	resp, err := j.runner.MediaDo(c.Request.Context(), http.MethodGet, "/v1/videos/"+j.engineID+"/content", c.Request.URL.Query(), nil, "")
	if err != nil {
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if passThrough(c, resp) && resp.StatusCode == http.StatusOK {
		j.endAfter(j.fetched)
	}
}

// VideoDeleteHandler serves DELETE /v1/videos/{id}: a running clip is
// cancelled, a finished one dropped, and the engine let go.
func (s *Server) VideoDeleteHandler(c *gin.Context) {
	j := videoJobOr404(c)
	if j == nil {
		return
	}
	if j.isLost() {
		j.end()
		c.JSON(http.StatusOK, map[string]any{"id": j.id, "object": "video.deleted", "deleted": true})
		return
	}
	resp, err := j.runner.MediaDo(c.Request.Context(), http.MethodDelete, "/v1/videos/"+j.engineID, nil, nil, "")
	if err != nil {
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		passThrough(c, resp)
		return
	}
	var obj map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		obj = map[string]any{"object": "video.deleted", "deleted": true}
	}
	obj["id"] = j.id
	j.end()
	c.JSON(http.StatusOK, obj)
}

// VideoListHandler serves GET /v1/videos: this server's jobs, newest first,
// as last seen.
func (s *Server) VideoListHandler(c *gin.Context) {
	videoJobs.mu.Lock()
	jobs := make([]*videoJob, 0, len(videoJobs.jobs))
	for _, j := range videoJobs.jobs {
		jobs = append(jobs, j)
	}
	videoJobs.mu.Unlock()
	slices.SortFunc(jobs, func(a, b *videoJob) int { return b.created.Compare(a.created) })
	data := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		j.mu.Lock()
		data = append(data, j.last)
		j.mu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// passThrough copies an engine answer to the client, headers that matter
// included, and reports whether it was written in full.
func passThrough(c *gin.Context, resp *http.Response) bool {
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			c.Header(h, v)
		}
	}
	c.Status(resp.StatusCode)
	_, err := io.Copy(c.Writer, resp.Body)
	return err == nil
}
