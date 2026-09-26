package server

// xollama: council roles on another server -- plans/agentic-council-chat.md
// (Phase 7). Additive; reached from councilMembers.Stream.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

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
	var out strings.Builder
	err = api.NewClient(u, http.DefaultClient).Chat(ctx, &req, func(resp api.ChatResponse) error {
		if t := resp.Message.Content; t != "" {
			out.WriteString(t)
			onToken(t)
		}
		if resp.Done {
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
