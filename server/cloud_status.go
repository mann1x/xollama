package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
)

// What a client reads of the cloud account pool (plans/cloud-accounts.md
// §3.6): enough to size its own concurrency, nothing that names a person.

// status is the pool now.
func (p *cloudPool) status() api.CloudPoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	if !p.on {
		return api.CloudPoolStatus{}
	}
	now := time.Now()
	st := api.CloudPoolStatus{Accounts: len(p.members), AccountsAtOnce: p.atOnce, Queued: len(p.waiters)}
	active := p.activeLocked(now, nil)
	counted := 0
	for _, m := range p.members {
		if p.atOnce == 0 || counted < p.atOnce {
			st.Parallel += m.parallel
			counted++
		}
		st.InUse += m.inUse
		a := api.CloudPoolAccount{Label: m.label, Plan: m.plan, Parallel: m.parallel, InUse: m.inUse, State: "ready"}
		switch {
		case now.Before(m.signedOutUntil):
			a.State, a.Until = "signed_out", timePtr(m.signedOutUntil)
			st.Exhausted++
		case now.Before(m.exhaustedUntil):
			a.State, a.Until = "exhausted", timePtr(m.exhaustedUntil)
			st.Exhausted++
		}
		for _, x := range active {
			if x == m {
				a.State = "active"
				st.ParallelNow += m.parallel
			}
		}
		st.Pool = append(st.Pool, a)
	}
	if t := p.earliestResetLocked(now); !t.IsZero() {
		st.NextReset = timePtr(t)
	}
	return st
}

func timePtr(t time.Time) *time.Time { u := t.UTC(); return &u }

// CloudPoolHandler is GET /api/xollama/cloud.
func (s *Server) CloudPoolHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, theCloudPool.status())
}

// cloudBalanceEvery is how often one account's balance is read at most:
// ollama.com allows 10 balance reads a minute per user, shared by every
// device of that user.
const cloudBalanceEvery = time.Minute

type cachedBalance struct {
	data []byte
	err  string
	at   time.Time
}

var cloudBalances = struct {
	sync.Mutex
	m map[string]cachedBalance
}{m: map[string]cachedBalance{}}

// CloudBalanceHandler is GET /api/xollama/cloud/balance: each pooled
// account's balance, read at most once a minute per account.
func (s *Server) CloudBalanceHandler(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	theCloudPool.mu.Lock()
	theCloudPool.refreshLocked()
	members := append([]*cloudMember(nil), theCloudPool.members...)
	theCloudPool.mu.Unlock()

	out := []api.CloudBalance{}
	for _, m := range members {
		b := cloudBalanceOf(c.Request.Context(), m)
		at := b.at.UTC()
		out = append(out, api.CloudBalance{Label: m.label, Balance: b.data, Error: b.err, ReadAt: &at})
	}
	c.JSON(http.StatusOK, out)
}

func cloudBalanceOf(ctx context.Context, m *cloudMember) cachedBalance {
	cloudBalances.Lock()
	defer cloudBalances.Unlock()
	if b, ok := cloudBalances.m[m.keyPath]; ok && time.Since(b.at) < cloudBalanceEvery {
		return b
	}
	cctx, cancel := context.WithTimeout(ctx, cloudCallTimeout)
	defer cancel()
	data, err := cloudBalanceRaw(cctx, m.keyPath)
	b := cachedBalance{data: data, at: time.Now()}
	if err != nil {
		b.err = err.Error()
	}
	cloudBalances.m[m.keyPath] = b
	return b
}
