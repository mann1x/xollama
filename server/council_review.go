package server

// xollama: the critics review the synthesizer's checks in the background --
// plans/agentic-council-chat.md 11.9. Additive; reached from the council
// handler in council.go and from place in council_polykv.go.
//
// A review outlives the trip that sent it: the synthesizer's turn suspends
// for the client's tool results while the critics are still reading. So the
// queue (council.Desk) is kept per conversation, with members of its own that
// carry no tools and run on their own sessions (~reviewer-N). It ends when
// the client leaves, or after reviewIdle with nothing sent.

import (
	"context"
	"sync"
	"time"

	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/llm"
)

// reviewIdle is how long a conversation's desk lives with nothing sent.
var reviewIdle = 15 * time.Minute

type reviewDesk struct {
	desk    *council.Desk
	cancel  context.CancelFunc
	timer   *time.Timer
	critics int
	// members are the desk's background members, whose usage the next
	// turn of the conversation reports.
	members *councilMembers
}

type deskRegistry struct {
	mu sync.Mutex
	m  map[string]*reviewDesk
}

var councilDesks = &deskRegistry{m: map[string]*reviewDesk{}}

// get is the conversation's desk, made with critics reviewers on members
// when there is none, and kept alive for reviewIdle more.
func (r *deskRegistry) get(session string, cfg council.Config, members *councilMembers) *council.Desk {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.m[session]; ok && d.critics == cfg.Critics {
		d.timer.Reset(reviewIdle)
		return d.desk
	} else if ok {
		d.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	bg := &councilMembers{
		s: members.s, base: members.base, session: session, window: members.window, cloud: members.cloud,
	}
	bg.base.Tools = nil
	if members.tree != nil {
		// On opencoti a request without a window books the whole session
		// pool; a reviewer states one sized to its request.
		bg.reviewWindow = members.tree.window
	}
	d := &reviewDesk{desk: council.NewDesk(ctx, bg, cfg, cfg.Critics), cancel: cancel, critics: cfg.Critics, members: bg}
	d.timer = time.AfterFunc(reviewIdle, func() { r.close(session) })
	r.m[session] = d
	return d.desk
}

// close ends the conversation's desk: its reviews in flight are cancelled.
func (r *deskRegistry) close(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.m[session]; ok {
		d.cancel()
		d.timer.Stop()
		delete(r.m, session)
	}
}

// ownWindow is the window of a member that shares nothing with the tree and
// runs on a session of its own -- a background reviewer, or the builder, which
// reads only the user's messages -- when the council runs on opencoti: its
// request, its reply cap and a margin, rounded, within the member window.
func (cm *councilMembers) ownWindow(r council.Request) *llm.Placement {
	window := cm.reviewWindow
	if window <= 0 && cm.tree != nil {
		window = cm.tree.window
	}
	if window <= 0 || (r.Role != council.Reviewer && r.Role != council.Builder) {
		return nil
	}
	n := 0
	for _, m := range r.Messages {
		n += len(m.Content)
	}
	size := min(roundUp(n/3+r.MaxTokens+512, 256), window)
	return &llm.Placement{NumCtx: size, NumCtxMin: size}
}
