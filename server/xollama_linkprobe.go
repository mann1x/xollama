package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// linkProbeTimeout bounds one backend's probe: opencoti copies 64 MiB x 8
// per run, about a second per GPU, plus starting the engine.
const linkProbeTimeout = 60 * time.Second

// linkProbeRun runs the engine's link probe for one backend and returns its
// stdout. A variable so a route test does not run the engine.
var linkProbeRun = func(ctx context.Context, b engine.Backend) ([]byte, error) {
	home, _ := os.UserHomeDir()
	artifact, err := engine.Find(envconfig.Var(engine.EnvPath), engine.DefaultDirs(ml.LibOllamaPath, home))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, linkProbeTimeout)
	defer cancel()
	name, args := engine.LinkProbeCommand(artifact, b, runtime.GOOS)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = envconfig.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, linkProbeError(stderr.String())
	}
	return out, nil
}

// linkProbeError is the engine's own reason for a failed probe: its last
// "opencoti --link-probe:" or "fatal error:" line. An engine without the
// probe prints neither -- it does not know the flag.
func linkProbeError(stderr string) error {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if r, ok := strings.CutPrefix(l, "opencoti --link-probe:"); ok {
			return errors.New(strings.TrimSpace(r))
		}
		if r, ok := strings.CutPrefix(l, "fatal error:"); ok {
			return errors.New(strings.TrimSpace(r))
		}
	}
	return errors.New("this engine has no link probe (opencoti b62 or later has it)")
}

// LinkProbeHandler measures the host link of the server's GPUs, one engine
// run per backend, for `xollama tweak server gpu`.
//
// A backend with a model generating on it is not probed: the probe shares
// the link with that engine, and the reading drops below the true link
// (opencoti #609). An idle loaded model does not move it.
func (s *Server) LinkProbeHandler(c *gin.Context) {
	if !loopbackPeer(c) || proxied(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "the link probe runs only for a client on the server's own machine, not through a proxy"})
		return
	}
	ctx := c.Request.Context()
	var backends []engine.Backend
	for _, g := range discoverDevices(ctx, nil) {
		b := engine.Backend(g.Library)
		if (b == engine.BackendCUDA || b == engine.BackendVulkan) && !slices.Contains(backends, b) {
			backends = append(backends, b)
		}
	}
	resp := api.LinkProbeResponse{Results: []api.LinkProbeResult{}}
	for _, b := range backends {
		r := api.LinkProbeResult{Backend: string(b)}
		if model := s.generatingOn(string(b)); model != "" {
			r.Error = fmt.Sprintf("not probed: %s is generating on a %s GPU, and would share the link", model, b)
			resp.Results = append(resp.Results, r)
			continue
		}
		out, err := linkProbeRun(ctx, b)
		if err == nil {
			var p api.LinkProbe
			if err = json.Unmarshal(bytes.TrimSpace(out), &p); err == nil {
				r.Probe = &p
			} else {
				err = fmt.Errorf("the engine's link probe answered something else: %w", err)
			}
		}
		if err != nil {
			r.Error = err.Error()
		}
		resp.Results = append(resp.Results, r)
	}
	c.JSON(http.StatusOK, resp)
}

// generatingOn names a model with a request in flight on a GPU of library,
// or "" when there is none.
func (s *Server) generatingOn(library string) string {
	if s.sched == nil {
		return ""
	}
	s.sched.loadedMu.Lock()
	defer s.sched.loadedMu.Unlock()
	for _, r := range s.sched.loaded {
		r.refMu.Lock()
		busy := r.refCount > 0
		r.refMu.Unlock()
		if busy && slices.ContainsFunc(r.gpus, func(d ml.DeviceID) bool { return d.Library == library }) {
			return r.modelPath
		}
	}
	return ""
}
