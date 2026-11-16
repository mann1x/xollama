package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/model"
)

// deleteUnloadWait bounds how long a delete waits for an idle runner of the
// model to leave the scheduler. The unload itself waits up to 5 s for the GPU
// memory to come back (waitForVRAMRecovery), so this is that plus the engine's
// own teardown.
var deleteUnloadWait = 30 * time.Second

// runnersNamed returns the loaded runners serving the model by that name. The
// scheduler keys runners by blob path, so a name is matched on the model each
// runner was loaded as.
func (s *Scheduler) runnersNamed(name string) []*runnerRef {
	if s == nil || name == "" {
		return nil
	}
	s.loadedMu.Lock()
	defer s.loadedMu.Unlock()
	var runners []*runnerRef
	for _, runner := range s.loaded {
		if m := runner.model.Load(); m != nil && m.Name == name {
			runners = append(runners, runner)
		}
	}
	return runners
}

// expireRunnersNamed asks every runner of the named model to unload: at once
// when it is idle, when its last request ends otherwise (keep_alive 0, the
// same path an unload request takes). It returns how many it found.
func (s *Scheduler) expireRunnersNamed(name string) int {
	runners := s.runnersNamed(name)
	for _, runner := range runners {
		runner.refMu.Lock()
		runner.expiresAt = time.Now()
		if runner.expireTimer != nil {
			runner.expireTimer.Stop()
			runner.expireTimer = nil
		}
		runner.sessionDuration = 0
		if runner.refCount <= 0 {
			s.expiredCh <- runner
		}
		runner.refMu.Unlock()
	}
	return len(runners)
}

// unloadBeforeDelete takes a model off the GPU before its manifest goes.
// Upstream unloads only from the CLI (`ollama rm` sends keep_alive 0 first);
// a delete through the API or the app left the runner resident, and with the
// manifest gone nothing could name it to unload it any more (mail #2307,
// eleven2go, 2026-11-16). A runner still serving a request unloads when that
// request ends; the delete does not wait for it.
func (s *Server) unloadBeforeDelete(n model.Name) {
	if s == nil || s.sched == nil || stockOnly() {
		return
	}
	name := n.String()
	if s.sched.expireRunnersNamed(name) == 0 {
		return
	}
	deadline := time.Now().Add(deleteUnloadWait)
	for time.Now().Before(deadline) {
		busy := false
		left := s.sched.runnersNamed(name)
		if len(left) == 0 {
			slog.Info("unloaded the model before deleting it", "model", name)
			return
		}
		for _, runner := range left {
			runner.refMu.Lock()
			if runner.refCount > 0 {
				busy = true
			}
			runner.refMu.Unlock()
		}
		if busy {
			slog.Info("deleting a model a request is still using; its runner unloads when the request ends", "model", name)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	slog.Warn("model still loaded after the delete's unload wait; deleting anyway", "model", name, "waited", deleteUnloadWait)
}

// unloadOrphan answers an unload request (keep_alive 0, nothing to generate)
// for a name that no longer resolves but still has a runner: a model deleted
// while a request was using it, or by a build without unloadBeforeDelete. It
// reports whether it answered.
func (s *Server) unloadOrphan(c *gin.Context, name string, keepAlive *api.Duration, empty bool) bool {
	if s == nil || s.sched == nil || stockOnly() || !empty || keepAlive == nil || keepAlive.Duration != 0 {
		return false
	}
	n := model.ParseName(name)
	if !n.IsValid() || s.sched.expireRunnersNamed(n.String()) == 0 {
		return false
	}
	slog.Info("unloading a runner whose model is no longer on disk", "model", n.String())
	c.JSON(http.StatusOK, gin.H{"model": name, "created_at": time.Now().UTC(), "done": true, "done_reason": "unload"})
	return true
}

// stockOnly is XOLLAMA_ENGINE=llamacpp, where a delete and an unload behave
// exactly as upstream's (CLAUDE.md rule 2).
func stockOnly() bool { return envconfig.Engine() == "llamacpp" }
