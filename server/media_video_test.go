package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/types/xollama"
)

func wanMedia(t *testing.T, fixed ...string) *xollama.Media {
	return &xollama.Media{Video: &xollama.VideoMedia{
		Model:       mediaDigest(t, []byte("wan")),
		Defaults:    &xollama.VideoDefaults{Width: 832, Height: 480, Frames: 33, FPS: 16, Steps: 30},
		MediaEngine: xollama.MediaEngine{Fixed: fixed},
	}}
}

// videoRouter serves the video routes as the server registers them.
func videoRouter(s *Server) *gin.Engine {
	r := gin.New()
	r.POST("/v1/videos", s.VideoCreateHandler)
	r.GET("/v1/videos", s.VideoListHandler)
	r.GET("/v1/videos/:id", s.VideoGetHandler)
	r.GET("/v1/videos/:id/content", s.VideoContentHandler)
	r.DELETE("/v1/videos/:id", s.VideoDeleteHandler)
	return r
}

func videoDo(t *testing.T, r http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// The request is over, as when a client hangs up after the create.
	cancel()
	var obj map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &obj)
	return w, obj
}

func shortVideoTimes(t *testing.T, poll, keep, fetched time.Duration) {
	p, k, f := videoPoll, videoKeep, videoFetched
	videoPoll, videoKeep, videoFetched = poll, keep, fetched
	t.Cleanup(func() { videoPoll, videoKeep, videoFetched = p, k, f })
}

// watchHolds records the context each new job holds its engine with.
func watchHolds(t *testing.T) func() context.Context {
	var mu sync.Mutex
	var last context.Context
	hold := videoHold
	videoHold = func(r context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := hold(r)
		mu.Lock()
		last = ctx
		mu.Unlock()
		return ctx, cancel
	}
	t.Cleanup(func() { videoHold = hold })
	return func() context.Context {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func waitDone(t *testing.T, ctx context.Context, what string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the engine is still held", what)
	}
}

func TestAVideoJobIsXollamasOwnAndHoldsItsEngineUntilTheClipIsFetched(t *testing.T) {
	mediaStore(t)
	shortVideoTimes(t, time.Hour, time.Hour, 20*time.Millisecond)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	r := videoRouter(s)
	held := watchHolds(t)

	w, obj := videoDo(t, r, http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"a cat on a boat"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	id, _ := obj["id"].(string)
	if !strings.HasPrefix(id, "video_") || id == "video_1" || obj["model"] != "wan" {
		t.Fatalf("create answered %v: want xollama's own id and the model", obj)
	}
	var sent map[string]any
	_ = json.Unmarshal(e.last(t).body, &sent)
	if sent["size"] != "832x480" || sent["steps"] != 30.0 || sent["fps"] != 16.0 {
		t.Errorf("the engine was sent %v, want the template's size, steps and fps", sent)
	}
	j := videoJobs.get(id)
	if j == nil {
		t.Fatal("the job is not in the table")
	}
	h := held()
	if h.Err() != nil {
		t.Fatal("the engine was let go when the create's request ended")
	}

	if w, obj := videoDo(t, r, http.MethodGet, "/v1/videos/"+id, ""); w.Code != http.StatusOK || obj["id"] != id || obj["status"] != "in_progress" {
		t.Fatalf("poll = %d %v", w.Code, obj)
	}
	e.videoStatus.Store("completed")
	if _, obj := videoDo(t, r, http.MethodGet, "/v1/videos/"+id, ""); obj["status"] != "completed" {
		t.Fatalf("poll after the clip = %v", obj)
	}
	if h.Err() != nil {
		t.Fatal("a finished clip let its engine go before it was fetched")
	}
	w, _ = videoDo(t, r, http.MethodGet, "/v1/videos/"+id+"/content", "")
	if w.Code != http.StatusOK || w.Body.String() != "MP4DATA" || w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("content = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	waitDone(t, h, "after the content")
	if w, _ := videoDo(t, r, http.MethodGet, "/v1/videos/"+id, ""); w.Code != http.StatusNotFound {
		t.Errorf("poll of a released job = %d, want 404", w.Code)
	}
}

func TestADeletedVideoLetsItsEngineGo(t *testing.T) {
	mediaStore(t)
	s, _ := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	r := videoRouter(s)
	held := watchHolds(t)
	_, obj := videoDo(t, r, http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"x"}`)
	id, _ := obj["id"].(string)
	j := videoJobs.get(id)
	if j == nil {
		t.Fatalf("create = %v", obj)
	}
	if _, list := videoDo(t, r, http.MethodGet, "/v1/videos", ""); len(list["data"].([]any)) == 0 {
		t.Errorf("the list does not have the job: %v", list)
	}
	w, obj := videoDo(t, r, http.MethodDelete, "/v1/videos/"+id, "")
	if w.Code != http.StatusOK || obj["id"] != id {
		t.Fatalf("delete = %d %v", w.Code, obj)
	}
	waitDone(t, held(), "after the delete")
	if videoJobs.get(id) != nil {
		t.Error("a deleted job stayed in the table")
	}
}

func TestAClipNobodyFetchesLetsItsEngineGoAfterTheKeep(t *testing.T) {
	mediaStore(t)
	shortVideoTimes(t, 10*time.Millisecond, 30*time.Millisecond, time.Hour)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	held := watchHolds(t)
	if w, _ := videoDo(t, videoRouter(s), http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("create = %d", w.Code)
	}
	e.videoStatus.Store("failed")
	waitDone(t, held(), "a finished clip nobody polls")
}

// An engine that dies under a clip leaves a failed job, not a 502 and then a
// job that was never there.
func TestAVideoWhoseEngineExitedIsAFailedJobNotAMissingOne(t *testing.T) {
	mediaStore(t)
	shortVideoTimes(t, 10*time.Millisecond, time.Hour, time.Hour)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	r := videoRouter(s)
	held := watchHolds(t)
	_, obj := videoDo(t, r, http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"x"}`)
	id, _ := obj["id"].(string)
	if videoJobs.get(id) == nil {
		t.Fatalf("create = %v", obj)
	}
	e.exited.Store(true)
	e.srv.CloseClientConnections()
	e.srv.Close()
	// Nobody polls: the watcher finds the engine gone and lets its slot go.
	waitDone(t, held(), "after the engine exited")

	w, got := videoDo(t, r, http.MethodGet, "/v1/videos/"+id, "")
	if w.Code != http.StatusOK || got["status"] != "failed" || got["id"] != id {
		t.Fatalf("status of a job whose engine exited = %d %v, want 200 failed", w.Code, got)
	}
	if er, _ := got["error"].(map[string]any); er["code"] != "engine_exited" {
		t.Errorf("error = %v, want code engine_exited", got["error"])
	}
	if w, _ := videoDo(t, r, http.MethodGet, "/v1/videos/"+id+"/content", ""); w.Code != http.StatusConflict {
		t.Errorf("content of a failed job = %d, want 409", w.Code)
	}
	if w, _ := videoDo(t, r, http.MethodDelete, "/v1/videos/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("delete of a failed job = %d", w.Code)
	}
	if videoJobs.get(id) != nil {
		t.Error("a deleted job stayed in the table")
	}
}

func TestAClipLargerThanTheTemplateIsRefused(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	r := videoRouter(s)
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"model":"wan","prompt":"x","size":"1280x720"}`, http.StatusBadRequest},
		{`{"model":"wan","prompt":"x","seconds":4}`, http.StatusBadRequest},
		{`{"model":"wan","prompt":"x","frames":"many"}`, http.StatusBadRequest},
		{`{"model":"wan","prompt":"x","seconds":"2"}`, http.StatusOK},
		{`{"model":"wan","prompt":"x","size":"480x832","seconds":2}`, http.StatusOK},
		{`{"model":"wan","prompt":"x","size":"640x352","frames":49}`, http.StatusOK},
	} {
		n := e.count()
		w, _ := videoDo(t, r, http.MethodPost, "/v1/videos", c.body)
		if w.Code != c.want {
			t.Errorf("%s = %d %s, want %d", c.body, w.Code, w.Body.String(), c.want)
		}
		if c.want != http.StatusOK && e.count() != n {
			t.Errorf("%s reached the engine", c.body)
		}
	}
}

func TestAFixedVideoSizeHoldsAgainstTheClient(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t, "width", "height")})
	if w, _ := videoDo(t, videoRouter(s), http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"x","size":"320x320"}`); w.Code != http.StatusOK {
		t.Fatalf("create = %d", w.Code)
	}
	var sent map[string]any
	_ = json.Unmarshal(e.last(t).body, &sent)
	if sent["size"] != "832x480" {
		t.Errorf("size = %v, want the fixed 832x480", sent["size"])
	}
}

func TestAFullVideoQueueIsPassedOnAndHoldsNothing(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"wan": wanMedia(t)})
	before := videoJobs.count()
	held := watchHolds(t)
	e.busy.Store(1)
	w, _ := videoDo(t, videoRouter(s), http.MethodPost, "/v1/videos", `{"model":"wan","prompt":"x"}`)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("create on a full queue = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if videoJobs.count() != before {
		t.Error("a refused create left a job")
	}
	if h := held(); h == nil || h.Err() == nil {
		t.Error("a refused create still holds the engine")
	}
}

func (e *fakeEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (t *videoTable) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.jobs)
}
