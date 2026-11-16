package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/model"
)

// loadedNamed puts an idle runner for the named model into a running
// scheduler, the state an API delete of a loaded model starts from.
func loadedNamed(t *testing.T, name string, refCount uint) (*Server, *runnerRef) {
	t.Helper()
	ctx, done := context.WithCancel(t.Context())
	t.Cleanup(done)
	sched := InitScheduler(ctx)
	sched.waitForRecovery = 10 * time.Millisecond
	runner := &runnerRef{
		llama:           &mockLlm{vramByGPU: map[ml.DeviceID]uint64{}},
		modelPath:       "/blobs/sha256-test",
		sessionDuration: 5 * time.Minute,
		refCount:        refCount,
	}
	m := &Model{Name: name, ModelPath: runner.modelPath}
	runner.model.Store(m)
	runner.modelKey = schedulerModelKey(m)
	sched.loaded[runner.modelKey] = runner
	go sched.processCompleted(ctx)
	return &Server{sched: sched}, runner
}

func loadedCount(s *Server) int {
	s.sched.loadedMu.Lock()
	defer s.sched.loadedMu.Unlock()
	return len(s.sched.loaded)
}

// mail #2307: a delete through the API left the runner on the GPU, and with
// the manifest gone no unload request could name it.
func TestDeleteUnloadsTheModelFirst(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	n := model.ParseName("mannix/gemma4-26b-agentic:mtp-vision-IQ2-qad")
	s, _ := loadedNamed(t, n.String(), 0)

	s.unloadBeforeDelete(n)

	if got := loadedCount(s); got != 0 {
		t.Fatalf("%d runner(s) still loaded after the delete's unload", got)
	}
}

func TestDeleteLeavesOtherModelsLoaded(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	s, _ := loadedNamed(t, model.ParseName("mannix/other:Q4_K_M").String(), 0)

	s.unloadBeforeDelete(model.ParseName("mannix/gemma4-26b-agentic:Q4_K_M"))

	if got := loadedCount(s); got != 1 {
		t.Fatalf("loaded = %d, want the other model kept", got)
	}
}

// A runner serving a request is not torn down under it: it is set to unload
// when the request ends, and the delete does not wait for that.
func TestDeleteOfABusyModelUnloadsWhenTheRequestEnds(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	n := model.ParseName("mannix/busy:Q4_K_M")
	s, runner := loadedNamed(t, n.String(), 1)

	start := time.Now()
	s.unloadBeforeDelete(n)
	if time.Since(start) > 5*time.Second {
		t.Fatal("the delete waited on a busy runner")
	}
	runner.refMu.Lock()
	d := runner.sessionDuration
	runner.refMu.Unlock()
	if d != 0 {
		t.Fatalf("sessionDuration = %v, want 0 so the runner unloads when its request ends", d)
	}
	if got := loadedCount(s); got != 1 {
		t.Fatalf("loaded = %d, want the busy runner kept until its request ends", got)
	}
}

func TestStockEngineDeleteDoesNotUnload(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "llamacpp")
	n := model.ParseName("mannix/stock:Q4_K_M")
	s, _ := loadedNamed(t, n.String(), 0)

	s.unloadBeforeDelete(n)

	if got := loadedCount(s); got != 1 {
		t.Fatalf("loaded = %d: XOLLAMA_ENGINE=llamacpp must keep upstream's delete", got)
	}
}

// The second half of #2307: keep_alive 0 for a name whose manifest is gone
// still unloads its runner instead of answering 404.
func TestUnloadByANameNoLongerOnDisk(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	gin.SetMode(gin.TestMode)
	name := "mannix/gone:Q4_K_M"
	s, _ := loadedNamed(t, model.ParseName(name).String(), 0)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if !s.unloadOrphan(c, name, &api.Duration{Duration: 0}, true) {
		t.Fatal("the unload request for the orphaned runner was not answered")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for loadedCount(s) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := loadedCount(s); got != 0 {
		t.Fatalf("%d runner(s) still loaded", got)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	if s.unloadOrphan(c, name, &api.Duration{Duration: 5 * time.Minute}, true) {
		t.Fatal("a request that is not an unload must keep upstream's 404")
	}
}

// Through the handler: fails if the delete-unload hook leaves DeleteHandler.
func TestDeleteHandlerUnloadsTheModel(t *testing.T) {
	t.Setenv("XOLLAMA_ENGINE", "")
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	gin.SetMode(gin.TestMode)
	s, _ := loadedNamed(t, model.ParseName("test").String(), 0)

	_, digest := createBinFile(t, nil, nil)
	if w := createRequest(t, s.CreateHandler, api.CreateRequest{Name: "test", Files: map[string]string{"test.gguf": digest}}); w.Code != http.StatusOK {
		t.Fatalf("create: %d", w.Code)
	}
	if w := createRequest(t, s.DeleteHandler, api.DeleteRequest{Name: "test"}); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if got := loadedCount(s); got != 0 {
		t.Fatalf("%d runner(s) still loaded after DELETE /api/delete", got)
	}
}
