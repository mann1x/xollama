package server

import (
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/council"
)

// usageBook adds up what a council's members spent, per role and per the
// model and host a role runs on (council_usage_v1). It answers the owner's
// question of which role is worth a bigger or a cloud model: a role's cost
// is the prompt it sends on every call, all of it billed by a cloud model,
// plus what it writes.
type usageBook struct {
	mu    sync.Mutex
	m     map[usageKey]*api.CouncilUsage
	order []usageKey
}

type usageKey struct{ role, model, host string }

// add records one member call that finished: its done chunk's metrics, the
// part of its prompt served from a cache, and its wall time.
func (b *usageBook) add(r council.Request, m api.Metrics, cached int, wall time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := usageKey{string(r.Role), r.Model, r.Host}
	u, ok := b.m[k]
	if !ok {
		if b.m == nil {
			b.m = map[usageKey]*api.CouncilUsage{}
		}
		u = &api.CouncilUsage{Role: k.role, Model: k.model, Host: k.host}
		b.m[k] = u
		b.order = append(b.order, k)
	}
	u.Calls++
	// prompt_eval_count counts only the tokens the engine computed; the
	// prompt sent is those plus the cached ones.
	u.PromptTokens += m.PromptEvalCount + cached
	u.CachedTokens += cached
	u.EvalTokens += m.EvalCount
	u.PromptDuration += m.PromptEvalDuration
	u.EvalDuration += m.EvalDuration
	u.Wall += wall
}

// take is what was recorded since the last take, in the order the roles
// first spent, and empties the book.
func (b *usageBook) take() []api.CouncilUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []api.CouncilUsage
	for _, k := range b.order {
		out = append(out, *b.m[k])
	}
	b.m, b.order = nil, nil
	return out
}

// usage is what the conversation's background reviewers spent since the
// last turn asked: they outlive the turn that sent their checks.
func (r *deskRegistry) usage(session string) []api.CouncilUsage {
	r.mu.Lock()
	d, ok := r.m[session]
	r.mu.Unlock()
	if !ok || d.members == nil {
		return nil
	}
	return d.members.usage.take()
}
