package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/types/xollama"
)

// The pool of Ollama Cloud accounts a cloud request is served through
// (plans/cloud-accounts.md §3.4).
//
// Each account has as many slots as it takes requests at once. The active
// accounts are the first accounts_at_once ready ones (primary first, then
// the added ones in order); a request takes a slot on the active account
// with the most free slots, and waits its turn when every slot is taken. An
// account that runs out (402, "usage limit") leaves the active set until it
// resets, and the next ready account takes its place. With no cloud section
// there is no pool, and every cloud request is upstream's.

// cloudExhaustedFor is how long an account that ran out is left alone when
// its balance does not say when it resets.
var cloudExhaustedFor = 15 * time.Minute

// cloudSignedOutFor is how long an account ollama.com refused (401) is left
// alone before it is tried again.
var cloudSignedOutFor = 10 * time.Minute

// cloudMember is one account in the pool. id "" is the primary.
type cloudMember struct {
	id, label, keyPath string
	plan               string
	parallel           int
	inUse              int
	exhaustedUntil     time.Time
	signedOutUntil     time.Time
}

func (m *cloudMember) ready(now time.Time) bool {
	return !now.Before(m.exhaustedUntil) && !now.Before(m.signedOutUntil)
}

func (m *cloudMember) free() int { return m.parallel - m.inUse }

// cloudWaiter is a request waiting for a slot.
type cloudWaiter struct {
	exclude []*cloudMember
	ch      chan struct{}
	lease   *cloudLease
	err     error
}

type cloudPool struct {
	mu      sync.Mutex
	sig     string
	on      bool
	atOnce  int
	members []*cloudMember
	waiters []*cloudWaiter
	timer   *time.Timer

	primaryPlan    string
	primaryAskedAt time.Time
}

var theCloudPool = &cloudPool{}

// cloudSettingsFn is the cloud section; tests replace it.
var cloudSettingsFn = serverCloud

// cloudLease is one slot on one account.
type cloudLease struct {
	p    *cloudPool
	m    *cloudMember
	once sync.Once
}

// ctx is parent signing as the lease's account.
func (l *cloudLease) ctx(parent context.Context) context.Context {
	return cloudCredCtx(parent, l.m.keyPath)
}

func (l *cloudLease) release() {
	l.once.Do(func() {
		l.p.mu.Lock()
		defer l.p.mu.Unlock()
		l.m.inUse--
		l.p.grantLocked()
	})
}

// errCloudExhausted is every account run out.
type errCloudExhausted struct{ until time.Time }

func (e errCloudExhausted) Error() string {
	if e.until.IsZero() {
		return "every Ollama Cloud account of this server has reached its usage limit or is not signed in"
	}
	return fmt.Sprintf("every Ollama Cloud account of this server has reached its usage limit or is not signed in; the first one is back at %s", e.until.UTC().Format(time.RFC3339))
}

// refreshLocked rebuilds the members when the settings changed, keeping
// each account's slots in use and its exhaustion.
func (p *cloudPool) refreshLocked() {
	cs := cloudSettingsFn()
	data, _ := json.Marshal(cs)
	sig := string(data) + "\x00" + p.primaryPlan
	if sig == p.sig {
		return
	}
	p.sig = sig
	if cs == nil {
		p.on, p.members = false, nil
		p.grantLocked()
		return
	}
	old := map[string]*cloudMember{}
	for _, m := range p.members {
		old[m.id] = m
	}
	var members []*cloudMember
	keep := func(m *cloudMember) {
		if o, ok := old[m.id]; ok {
			o.label, o.keyPath, o.parallel, o.plan = m.label, m.keyPath, m.parallel, m.plan
			m = o
		}
		members = append(members, m)
	}
	if cs.PrimaryIncluded() {
		keep(&cloudMember{label: "primary", plan: p.primaryPlan, parallel: cs.AccountParallel(xollama.CloudAccount{Plan: p.primaryPlan})})
	}
	for _, a := range cs.Accounts {
		if !a.Usable() {
			continue
		}
		kp, err := cloudCredPath(a)
		if err != nil {
			continue
		}
		keep(&cloudMember{id: a.ID, label: a.Label, keyPath: kp, plan: a.Plan, parallel: cs.AccountParallel(a)})
	}
	p.on, p.atOnce, p.members = true, cs.AccountsAtOnce, members
	p.askPrimaryPlanLocked(cs)
	p.grantLocked()
}

// askPrimaryPlanLocked learns the primary's plan in the background, at most
// once an hour, so its slots follow its plan.
func (p *cloudPool) askPrimaryPlanLocked(cs *xollama.CloudSettings) {
	if !cs.PrimaryIncluded() || time.Since(p.primaryAskedAt) < time.Hour {
		return
	}
	p.primaryAskedAt = time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cloudCallTimeout)
		defer cancel()
		u, err := cloudWhoami(ctx, "")
		if err != nil || u == nil || u.Name == "" {
			return
		}
		plan := strings.TrimSpace(u.Plan)
		if plan == "" {
			plan = "free"
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if plan != p.primaryPlan {
			p.primaryPlan = plan
			p.refreshLocked()
		}
	}()
}

// activeLocked is the first accounts_at_once ready members not excluded.
func (p *cloudPool) activeLocked(now time.Time, exclude []*cloudMember) []*cloudMember {
	var out []*cloudMember
	for _, m := range p.members {
		if !m.ready(now) || slices.Contains(exclude, m) {
			continue
		}
		if p.atOnce > 0 && len(out) == p.atOnce {
			break
		}
		out = append(out, m)
	}
	return out
}

// pickLocked is the active member with the most free slots, nil when every
// slot is taken.
func (p *cloudPool) pickLocked(now time.Time, exclude []*cloudMember) *cloudMember {
	var best *cloudMember
	for _, m := range p.activeLocked(now, exclude) {
		if m.free() > 0 && (best == nil || m.free() > best.free()) {
			best = m
		}
	}
	return best
}

// earliestResetLocked is when the first run-out account comes back.
func (p *cloudPool) earliestResetLocked(now time.Time) time.Time {
	var t time.Time
	for _, m := range p.members {
		for _, u := range []time.Time{m.exhaustedUntil, m.signedOutUntil} {
			if u.After(now) && (t.IsZero() || u.Before(t)) {
				t = u
			}
		}
	}
	return t
}

// grantLocked hands free slots to waiters in order, fails them all when no
// account is ready, and wakes the pool when the first account resets.
func (p *cloudPool) grantLocked() {
	now := time.Now()
	if !p.on {
		for _, w := range p.waiters {
			close(w.ch)
		}
		p.waiters = nil
		return
	}
	if len(p.activeLocked(now, nil)) == 0 {
		err := errCloudExhausted{p.earliestResetLocked(now)}
		for _, w := range p.waiters {
			w.err = err
			close(w.ch)
		}
		p.waiters = nil
	}
	var left []*cloudWaiter
	for _, w := range p.waiters {
		if m := p.pickLocked(now, w.exclude); m != nil {
			m.inUse++
			w.lease = &cloudLease{p: p, m: m}
			close(w.ch)
			continue
		}
		left = append(left, w)
	}
	p.waiters = left
	if t := p.earliestResetLocked(now); !t.IsZero() {
		if p.timer != nil {
			p.timer.Stop()
		}
		p.timer = time.AfterFunc(time.Until(t)+10*time.Millisecond, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.grantLocked()
		})
	}
}

// acquire takes a slot, waiting its turn. A nil lease with a nil error is no
// pool: the request is upstream's.
func (p *cloudPool) acquire(ctx context.Context, exclude []*cloudMember) (*cloudLease, error) {
	p.mu.Lock()
	p.refreshLocked()
	if !p.on {
		p.mu.Unlock()
		return nil, nil
	}
	now := time.Now()
	if len(p.activeLocked(now, nil)) == 0 {
		err := errCloudExhausted{p.earliestResetLocked(now)}
		p.mu.Unlock()
		return nil, err
	}
	if len(p.waiters) == 0 {
		if m := p.pickLocked(now, exclude); m != nil {
			m.inUse++
			p.mu.Unlock()
			return &cloudLease{p: p, m: m}, nil
		}
	}
	w := &cloudWaiter{exclude: exclude, ch: make(chan struct{})}
	p.waiters = append(p.waiters, w)
	p.mu.Unlock()

	select {
	case <-w.ch:
		return w.lease, w.err
	case <-ctx.Done():
		p.mu.Lock()
		defer p.mu.Unlock()
		if i := slices.Index(p.waiters, w); i >= 0 {
			p.waiters = slices.Delete(p.waiters, i, i+1)
			return nil, ctx.Err()
		}
		// Granted while giving up: give the slot back.
		if w.lease != nil {
			w.lease.m.inUse--
			p.grantLocked()
		}
		return nil, ctx.Err()
	}
}

// capacity is the requests at once the pool serves now: the slots of its
// active accounts. 0 without a pool.
func (p *cloudPool) capacity() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	if !p.on {
		return 0
	}
	n := 0
	for _, m := range p.activeLocked(time.Now(), nil) {
		n += m.parallel
	}
	return n
}

// readyBeyond says whether a ready account is left besides those tried.
func (p *cloudPool) readyBeyond(tried []*cloudMember) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, m := range p.members {
		if m.ready(now) && !slices.Contains(tried, m) {
			return true
		}
	}
	return false
}

// tryAcquire takes a free slot now, or none.
func (p *cloudPool) tryAcquire(exclude []*cloudMember) *cloudLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	if !p.on || len(p.waiters) > 0 {
		return nil
	}
	if m := p.pickLocked(time.Now(), exclude); m != nil {
		m.inUse++
		return &cloudLease{p: p, m: m}
	}
	return nil
}

func (p *cloudPool) markExhausted(m *cloudMember, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(m.exhaustedUntil) {
		m.exhaustedUntil = until
	}
	slog.Warn("cloud account reached its usage limit; the next account serves", "account", m.label, "until", m.exhaustedUntil.UTC().Format(time.RFC3339))
	p.grantLocked()
}

func (p *cloudPool) markSignedOut(m *cloudMember) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m.signedOutUntil = time.Now().Add(cloudSignedOutFor)
	slog.Warn("ollama.com refused a cloud account's key; it is left out for a while", "account", m.label, "retry_after", cloudSignedOutFor)
	p.grantLocked()
}

// cloudOutcome is what a failed answer from ollama.com means for the account.
type cloudOutcome int

const (
	cloudPass      cloudOutcome = iota // the client's to see
	cloudExhausted                     // the account ran out: next account
	cloudBusy                          // too many requests: another account with a free slot
	cloudSignedOut                     // the key is not accepted: next account
)

var usageLimitText = regexp.MustCompile(`(?i)usage limit|upgrade for higher limits|insufficient (?:credit|balance)`)

// classifyCloud reads a status and its body.
func classifyCloud(status int, body string) cloudOutcome {
	switch {
	case status < 400:
		return cloudPass
	case status == http.StatusPaymentRequired, usageLimitText.MatchString(body):
		return cloudExhausted
	case status == http.StatusTooManyRequests:
		return cloudBusy
	case status == http.StatusUnauthorized:
		return cloudSignedOut
	}
	return cloudPass
}

// handleOutcome marks the account for an outcome, and says whether another
// account may be tried.
func (p *cloudPool) handleOutcome(m *cloudMember, o cloudOutcome) bool {
	switch o {
	case cloudExhausted:
		p.markExhausted(m, time.Now().Add(cloudExhaustedFor))
		go p.learnReset(m, cloudBalanceResets)
		return true
	case cloudSignedOut:
		p.markSignedOut(m)
		return true
	case cloudBusy:
		return true
	}
	return false
}

// cloudBalanceResets reads when an account that ran out resets, from its
// /api/balance; zero when it does not say. Tests replace it.
var cloudBalanceResets = func(ctx context.Context, keyPath string) (time.Time, error) {
	data, err := cloudBalanceRaw(ctx, keyPath)
	if err != nil {
		return time.Time{}, err
	}
	var b cloudBalance
	if err := json.Unmarshal(data, &b); err != nil {
		return time.Time{}, err
	}
	return b.resetsAt(), nil
}

// cloudBalanceRaw is the account's /api/balance from ollama.com, signed
// with its key ("" is the primary's). Tests replace it.
var cloudBalanceRaw = func(ctx context.Context, keyPath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cloudProxyBaseURL, "/")+"/api/balance", nil)
	if err != nil {
		return nil, err
	}
	sctx := ctx
	sctx = cloudCredCtx(sctx, keyPath)
	if err := cloudProxySignRequest(sctx, req); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama.com answered %s", resp.Status)
	}
	return data, nil
}

// cloudBalance is the part of /api/balance that says when an account that
// ran out comes back (the fields Cerebriline's parseOllamaBalance reads).
type cloudBalance struct {
	Included struct {
		BalanceUSD *float64 `json:"balance_usd"`
		Period     struct {
			Until string `json:"until"`
		} `json:"period"`
		Session *cloudWindow `json:"session"`
		Weekly  *cloudWindow `json:"weekly"`
	} `json:"included"`
	Purchased struct {
		BalanceUSD *float64 `json:"balance_usd"`
	} `json:"purchased"`
}

type cloudWindow struct {
	RemainingPercent *float64 `json:"remaining_percent"`
	ResetsAt         string   `json:"resets_at"`
}

// resetsAt is the latest reset among the allowances that are spent: the
// account is back only once every spent one has reset.
func (b cloudBalance) resetsAt() time.Time {
	var out time.Time
	take := func(s string) {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(out) {
			out = t
		}
	}
	for _, w := range []*cloudWindow{b.Included.Session, b.Included.Weekly} {
		if w != nil && w.RemainingPercent != nil && *w.RemainingPercent <= 0 {
			take(w.ResetsAt)
		}
	}
	spent := func(v *float64) bool { return v != nil && *v <= 0 }
	if spent(b.Included.BalanceUSD) && (b.Purchased.BalanceUSD == nil || spent(b.Purchased.BalanceUSD)) {
		take(b.Included.Period.Until)
	}
	return out
}

// learnReset moves an exhausted account's return to its balance's reset.
func (p *cloudPool) learnReset(m *cloudMember, resets func(context.Context, string) (time.Time, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudCallTimeout)
	defer cancel()
	t, err := resets(ctx, m.keyPath)
	if err != nil || t.IsZero() || !t.After(time.Now()) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m.exhaustedUntil = t
	p.grantLocked()
}

// errors.Is support for callers.
func isCloudExhausted(err error) bool {
	var e errCloudExhausted
	return errors.As(err, &e)
}
