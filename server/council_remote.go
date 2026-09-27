package server

// xollama: council roles on another server -- plans/agentic-council-chat.md
// (Phase 7). Additive; reached from councilMembers.Stream.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/council"
)

// remote serves one member on the server its role names. The request is the
// one a local member would send, less what only this server's engine
// understands: no session, and so no placement or pool.
func (cm *councilMembers) remote(ctx context.Context, r council.Request, req api.ChatRequest, onToken func(string)) (string, error) {
	u, err := councilHostAllowed(r.Host, envconfig.CouncilHosts())
	if err != nil {
		return "", fmt.Errorf("council %s: %w", r.Role, err)
	}
	req.SessionID = ""
	think := any(nil)
	if req.Think != nil {
		think = req.Think.Value
	}
	slog.Info("council: member on another server", "role", r.Role, "index", r.Index, "host", u.Host, "model", req.Model, "think", think)
	var out strings.Builder
	done := false
	err = api.NewClient(u, http.DefaultClient).Chat(ctx, &req, func(resp api.ChatResponse) error {
		if t := resp.Message.Content; t != "" {
			out.WriteString(t)
			onToken(t)
		}
		if resp.Done {
			done = true
			cm.mu.Lock()
			cm.m.PromptEvalCount += resp.PromptEvalCount
			cm.m.PromptEvalDuration += resp.PromptEvalDuration
			cm.m.EvalCount += resp.EvalCount
			cm.m.EvalDuration += resp.EvalDuration
			cm.m.LoadDuration += resp.LoadDuration
			cm.mu.Unlock()
		}
		return nil
	})
	// A connection that drops mid-reply ends the stream without an error; only
	// the final done line says the member finished. Without it the reply is a
	// fragment, and the turn would go on as if it were whole (measured: a
	// tunnel dropped mid-research left the council with no findings at all).
	if err == nil && !done && ctx.Err() == nil {
		err = errors.New("the stream ended before the member finished")
	}
	if err != nil {
		return out.String(), fmt.Errorf("council %s on %s at %s: %w", r.Role, req.Model, u.Host, err)
	}
	return out.String(), nil
}

// councilHostAllowed parses a role's host and checks it against the operator's
// allow-list: host names or host:port, "*" for any.
func councilHostAllowed(host, allowed string) (*url.URL, error) {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("host %q is not a URL", host)
	}
	list := strings.Split(allowed, ",")
	for i := range list {
		list[i] = strings.ToLower(strings.TrimSpace(list[i]))
	}
	name := strings.ToLower(u.Hostname())
	hostPort := name
	if p := u.Port(); p != "" {
		hostPort = net.JoinHostPort(name, p)
	}
	if slices.Contains(list, "*") || slices.Contains(list, name) || slices.Contains(list, hostPort) {
		return u, nil
	}
	return nil, fmt.Errorf("host %s is not in XOLLAMA_COUNCIL_HOSTS; the server's operator allows the servers a council may send a conversation to", u.Host)
}

// councilTakesBudget reports whether a member's server accepts a think budget
// as a token count. Only xollama does, and only for a model it serves itself:
// a cloud model goes to ollama.com, which refuses one ("think must be a
// boolean or string"), and a stock ollama has no budget at all. Everywhere
// else the member is sent think true, and num_predict bounds it.
func (cm *councilMembers) councilTakesBudget(ctx context.Context, r council.Request) bool {
	if r.Host == "" {
		return r.Model == "" || !councilModelIsCloud(r.Model)
	}
	u, err := url.Parse(r.Host)
	if err != nil {
		return false
	}
	return councilProbes.takesBudget(ctx, u, r.Model)
}

// councilModelIsCloud is whether this server serves name from the cloud: a
// cloud reference ("…:cloud"), or a pulled tag whose manifest names a remote
// host -- the discriminator, since a cloud tag need not say so in its name.
var councilModelIsCloud = func(name string) bool {
	if councilIsCloudRef(name) {
		return true
	}
	m, err := GetModel(name)
	return err == nil && (m.Config.RemoteHost != "" || m.Config.RemoteModel != "")
}

// councilProbes caches what another server said about itself and its models,
// so a turn does not ask again for every member.
var councilProbes = &hostProbes{ttl: 5 * time.Minute, entries: map[string]probeEntry{}}

type hostProbes struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]probeEntry
}

type probeEntry struct {
	ok bool
	at time.Time
}

func (h *hostProbes) takesBudget(ctx context.Context, u *url.URL, model string) bool {
	key := u.String() + "\x00" + model
	h.mu.Lock()
	if e, ok := h.entries[key]; ok && time.Since(e.at) < h.ttl {
		h.mu.Unlock()
		return e.ok
	}
	h.mu.Unlock()

	// A cloud reference is cloud on any server, and there /api/show answers
	// from ollama.com, with no remote_host to say so (measured on a
	// 0.34.2-xollama.1: gemma4:31b-cloud showed as a plain gemma4). Only a
	// pulled tag's show carries it.
	ok := false
	if !councilIsCloudRef(model) && api.IsXollama(ctx, u) {
		show, err := api.NewClient(u, http.DefaultClient).Show(ctx, &api.ShowRequest{Model: model})
		ok = err == nil && show.RemoteHost == "" && show.RemoteModel == ""
	}
	h.mu.Lock()
	h.entries[key] = probeEntry{ok: ok, at: time.Now()}
	h.mu.Unlock()
	return ok
}

func (h *hostProbes) reset() {
	h.mu.Lock()
	h.entries = map[string]probeEntry{}
	h.mu.Unlock()
}

// councilIsCloudRef is whether a model name is itself a cloud reference
// ("…:cloud", "…-cloud"), which every server sends to ollama.com.
func councilIsCloudRef(name string) bool {
	ref, err := parseAndValidateModelRef(name)
	return err == nil && ref.Source == modelSourceCloud
}
