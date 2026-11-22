package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/auth"
	"github.com/ollama/ollama/types/xollama"
)

// testPool gives the test a fresh pool over cs, with keys made under a
// temporary home for every account, and returns each account's public key.
func testPool(t *testing.T, cs *xollama.CloudSettings) map[string]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pubs := map[string]string{}
	for i := range cs.Accounts {
		a := &cs.Accounts[i]
		if a.VerifiedAt == "" && !a.SignedOut {
			a.VerifiedAt = "2026-11-22T00:00:00Z"
		}
		pub, err := newCloudKey(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		pubs[a.Label] = pub
	}
	oldPool, oldFn := theCloudPool, cloudSettingsFn
	theCloudPool = &cloudPool{}
	cloudSettingsFn = func() *xollama.CloudSettings { return cs }
	t.Cleanup(func() { theCloudPool, cloudSettingsFn = oldPool, oldFn })
	return pubs
}

func accounts(parallel int, labels ...string) []xollama.CloudAccount {
	var out []xollama.CloudAccount
	for i, l := range labels {
		out = append(out, xollama.CloudAccount{ID: "0000000" + string(rune('a'+i)), Label: l, Plan: "pro", Parallel: parallel})
	}
	return out
}

func TestWithoutACloudSectionThereIsNoPool(t *testing.T) {
	cloudSettingsFn, theCloudPool = func() *xollama.CloudSettings { return nil }, &cloudPool{}
	t.Cleanup(func() { cloudSettingsFn, theCloudPool = serverCloud, &cloudPool{} })
	l, err := theCloudPool.acquire(context.Background(), nil)
	if l != nil || err != nil {
		t.Fatalf("acquire = %v, %v; want no lease and no error (upstream's path)", l, err)
	}
	if st := theCloudPool.status(); st.Accounts != 0 || st.Pool != nil {
		t.Fatalf("status = %+v, want accounts 0 only", st)
	}
	if n := councilCloudParallel(nil); n != xollama.DefaultCouncilCloudParallel {
		t.Fatalf("council cloud_parallel = %d, want the default %d", n, xollama.DefaultCouncilCloudParallel)
	}
}

func TestSlotsQueueInOrderAndAccountsAtOnceBoundTheActiveSet(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, AccountsAtOnce: 2, Accounts: accounts(2, "a", "b", "c")}
	testPool(t, cs)
	if n := theCloudPool.capacity(); n != 4 {
		t.Fatalf("2 accounts at once x 2 = %d, want 4", n)
	}
	var leases []*cloudLease
	for range 4 {
		l, err := theCloudPool.acquire(context.Background(), nil)
		if err != nil || l == nil {
			t.Fatalf("acquire: %v %v", l, err)
		}
		if l.m.label == "c" {
			t.Fatal("c took a request while beyond accounts_at_once")
		}
		leases = append(leases, l)
	}
	// Every slot taken: the next two wait, in order.
	got := make(chan string, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := theCloudPool.acquire(context.Background(), nil)
			if err != nil {
				t.Error(err)
				return
			}
			got <- string(rune('1' + i))
			l.release()
		}()
		for theCloudPool.status().Queued != i+1 {
			time.Sleep(time.Millisecond)
		}
	}
	if st := theCloudPool.status(); st.Queued != 2 || st.InUse != 4 {
		t.Fatalf("status = %+v, want 2 queued, 4 in use", st)
	}
	leases[0].release()
	if first := <-got; first != "1" {
		t.Fatalf("the second waiter went first")
	}
	leases[1].release()
	<-got
	wg.Wait()

	// An account that runs out leaves the active set; c takes its place.
	theCloudPool.markExhausted(leases[2].m, time.Now().Add(time.Hour))
	st := theCloudPool.status()
	if st.ParallelNow != 4 || st.Exhausted != 1 {
		t.Fatalf("after one ran out: %+v, want c in its place (4 now, 1 exhausted)", st)
	}
	for _, a := range st.Pool {
		if a.Label == "c" && a.State != "active" {
			t.Fatalf("c is %s, want active", a.State)
		}
	}
}

func TestAnExhaustedAccountComesBackAtItsReset(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: accounts(1, "a")}
	testPool(t, cs)
	l, _ := theCloudPool.acquire(context.Background(), nil)
	theCloudPool.markExhausted(l.m, time.Now().Add(50*time.Millisecond))
	l.release()
	if _, err := theCloudPool.acquire(context.Background(), nil); !isCloudExhausted(err) {
		t.Fatalf("acquire with every account out: %v, want errCloudExhausted", err)
	}
	time.Sleep(80 * time.Millisecond)
	l, err := theCloudPool.acquire(context.Background(), nil)
	if err != nil || l == nil {
		t.Fatalf("after the reset: %v %v, want the account back", l, err)
	}
	l.release()
}

func TestABalanceSaysWhenASpentAccountResets(t *testing.T) {
	var b cloudBalance
	if err := json.Unmarshal([]byte(`{"included":{"balance_usd":3.2,"session":{"remaining_percent":0,"resets_at":"2026-11-22T20:00:00Z"},"weekly":{"remaining_percent":40,"resets_at":"2026-11-27T00:00:00Z"}}}`), &b); err != nil {
		t.Fatal(err)
	}
	if got := b.resetsAt().Format(time.RFC3339); got != "2026-11-22T20:00:00Z" {
		t.Fatalf("reset = %s, want the spent session's", got)
	}
}

// fakeCloud is ollama.com for the passthrough: it answers each account (told
// apart by the public key in Authorization) with the status the test sets.
type fakeCloud struct {
	mu     sync.Mutex
	status map[string]int // by label
	byKey  map[string]string
	seen   []string
}

func (f *fakeCloud) serve(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("Authorization")
	label := "?"
	for pub, l := range f.byKey {
		if strings.HasPrefix(tok, pub+":") {
			label = l
		}
	}
	f.mu.Lock()
	f.seen = append(f.seen, label)
	st := f.status[label]
	f.mu.Unlock()
	if st == 0 {
		st = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	switch st {
	case http.StatusPaymentRequired:
		io.WriteString(w, `{"error":"you have reached your session usage limit"}`)
	case http.StatusTooManyRequests:
		io.WriteString(w, `{"error":"too many concurrent requests"}`)
	default:
		io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi from `+label+`"},"done":true}`)
	}
}

func withFakeCloud(t *testing.T, pubs map[string]string) *fakeCloud {
	t.Helper()
	f := &fakeCloud{status: map[string]int{}, byKey: map[string]string{}}
	for l, p := range pubs {
		f.byKey[strings.Fields(p)[1]] = l
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	oldBase, oldHost := cloudProxyBaseURL, cloudProxySigningHost
	cloudProxyBaseURL, cloudProxySigningHost = srv.URL, u.Hostname()
	t.Cleanup(func() { cloudProxyBaseURL, cloudProxySigningHost = oldBase, oldHost })
	oldResets := cloudBalanceResets
	cloudBalanceResets = func(context.Context, string) (time.Time, error) { return time.Time{}, nil }
	t.Cleanup(func() { cloudBalanceResets = oldResets })
	return f
}

func chatThroughProxy(t *testing.T) (int, string) {
	t.Helper()
	s := &Server{}
	router, err := s.GenerateRoutes()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"m:cloud","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestARunOutAccountsRequestIsSentAgainOnTheNextAccount(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: accounts(1, "a", "b")}
	f := withFakeCloud(t, testPool(t, cs))
	f.status["a"] = http.StatusPaymentRequired
	code, body := chatThroughProxy(t)
	if code != http.StatusOK || !strings.Contains(body, "hi from b") {
		t.Fatalf("chat = %d %s, want b's answer", code, body)
	}
	if strings.Join(f.seen, ",") != "a,b" {
		t.Fatalf("ollama.com saw %v, want a then b", f.seen)
	}
	st := theCloudPool.status()
	if st.Exhausted != 1 || st.InUse != 0 {
		t.Fatalf("status = %+v, want a exhausted and every slot back", st)
	}
	// a is out: the next request goes straight to b.
	f.seen = nil
	if code, body := chatThroughProxy(t); code != http.StatusOK || strings.Join(f.seen, ",") != "b" {
		t.Fatalf("second chat = %d %s via %v, want b alone", code, body, f.seen)
	}
	// Both out: ollama.com's 402, naming the pool.
	f.status["b"] = http.StatusPaymentRequired
	chatThroughProxy(t)
	code, body = chatThroughProxy(t)
	if code != http.StatusPaymentRequired || !strings.Contains(body, "every Ollama Cloud account") {
		t.Fatalf("all out: %d %s, want 402 naming the pool", code, body)
	}
}

func TestA429NeverMarksAnAccountExhausted(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: accounts(1, "a")}
	f := withFakeCloud(t, testPool(t, cs))
	f.status["a"] = http.StatusTooManyRequests
	if code, _ := chatThroughProxy(t); code != http.StatusTooManyRequests {
		t.Fatalf("chat = %d, want the 429 passed on (no other account)", code)
	}
	if st := theCloudPool.status(); st.Exhausted != 0 || st.InUse != 0 {
		t.Fatalf("status = %+v, want nothing exhausted and the slot back", st)
	}
}

func TestEachAccountSignsWithItsOwnKey(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: accounts(1, "a", "b")}
	f := withFakeCloud(t, testPool(t, cs))
	l1, _ := theCloudPool.acquire(context.Background(), nil)
	defer l1.release()
	chatThroughProxy(t) // a is held: the request goes to b
	if len(f.seen) != 1 || f.seen[0] == "?" || f.seen[0] == l1.m.label {
		t.Fatalf("ollama.com saw %v with %s held, want the other account's key", f.seen, l1.m.label)
	}
}

func TestAPulledCloudModelIsSentAgainOnlyBeforeAnythingWasWritten(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: accounts(1, "a", "b")}
	testPool(t, cs)
	withFakeCloud(t, nil)
	remote, _ := url.Parse(cloudProxyBaseURL)
	keyA, _ := cloudKeyPath(cs.Accounts[0].ID)

	call := func(write bool) ([]string, error) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/chat", nil)
		var used []string
		err := cloudStubCall(c, remote, func(ctx context.Context) error {
			p, _ := auth.KeyFileFrom(ctx)
			used = append(used, filepath.Base(filepath.Dir(p)))
			if p == keyA {
				if write {
					c.Writer.WriteString("partial")
				}
				return api.StatusError{StatusCode: http.StatusPaymentRequired, ErrorMessage: "you have reached your session usage limit"}
			}
			return nil
		})
		return used, err
	}
	used, err := call(false)
	if err != nil || len(used) != 2 {
		t.Fatalf("before any write: err %v, accounts %v; want a then b, no error", err, used)
	}
	theCloudPool = &cloudPool{}
	used, err = call(true)
	var se api.StatusError
	if !errors.As(err, &se) || len(used) != 1 {
		t.Fatalf("after a write: err %v, accounts %v; want a's 402 passed on", err, used)
	}
}

func TestTheStatusRouteNamesNoPerson(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, AccountsAtOnce: 2, Accounts: accounts(0, "a", "b", "c")}
	testPool(t, cs)
	s := &Server{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(api.XollamaCloudPath, s.CloudPoolHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, api.XollamaCloudPath, nil))
	var st api.CloudPoolStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Accounts != 3 || st.Parallel != 6 || st.ParallelNow != 6 {
		t.Fatalf("status = %+v, want 3 accounts, 2 at once x pro 3 = 6", st)
	}
	home, _ := os.UserHomeDir()
	for _, leak := range []string{"0000000a", "@", home, "key"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("status names %q: %s", leak, w.Body.String())
		}
	}
	if n := councilCloudParallel(nil); n != 6 {
		t.Fatalf("council cloud_parallel with a pool = %d, want the pool's 6", n)
	}
}

func TestAPooledAPIKeyAccountSendsItsKey(t *testing.T) {
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: []xollama.CloudAccount{
		{ID: "0000000a", Label: "k", Plan: "free", Auth: xollama.CloudAuthAPIKey, VerifiedAt: "2026-11-22T00:00:00Z"},
	}}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, err := writeCloudAPIKey("0000000a", "pooled.key", false); err != nil {
		t.Fatal(err)
	}
	oldPool, oldFn := theCloudPool, cloudSettingsFn
	theCloudPool, cloudSettingsFn = &cloudPool{}, func() *xollama.CloudSettings { return cs }
	t.Cleanup(func() { theCloudPool, cloudSettingsFn = oldPool, oldFn })

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	oldBase, oldHost := cloudProxyBaseURL, cloudProxySigningHost
	cloudProxyBaseURL, cloudProxySigningHost = srv.URL, u.Hostname()
	t.Cleanup(func() { cloudProxyBaseURL, cloudProxySigningHost = oldBase, oldHost })

	if code, body := chatThroughProxy(t); code != http.StatusOK {
		t.Fatalf("chat = %d %s", code, body)
	}
	if len(got) != 1 || got[0] != "Bearer pooled.key" {
		t.Fatalf("ollama.com saw Authorization %q, want the account's API key", got)
	}
}

func TestAConfiguredPoolNeverReadsAsNoPool(t *testing.T) {
	acc := accounts(0, "a", "b", "c")
	acc[1].SignedOut = true
	acc[2].Disabled = true
	cs := &xollama.CloudSettings{Primary: xollama.CloudPrimaryExclude, Accounts: acc}
	testPool(t, cs)
	st := theCloudPool.status()
	if st.Accounts != 3 || st.ParallelNow != 3 {
		t.Fatalf("status = %+v, want 3 accounts listed, a's 3 usable", st)
	}
	states := map[string]string{}
	for _, a := range st.Pool {
		states[a.Label] = a.State
	}
	if states["a"] != "active" || states["b"] != "signed_out" || states["c"] != "disabled" {
		t.Fatalf("states = %v", states)
	}
	// Nothing usable: still a pool, with nothing to serve now.
	cs.Accounts[0].SignedOut = true
	if st := theCloudPool.status(); st.Accounts != 3 || st.ParallelNow != 0 {
		t.Fatalf("nothing usable: %+v, want accounts 3, parallel_now 0", st)
	}
}

func TestAPrimaryThatIsNotSignedInTakesNoRequests(t *testing.T) {
	cs := &xollama.CloudSettings{Accounts: accounts(0, "a")}
	testPool(t, cs)
	asked := make(chan struct{}, 4)
	old := cloudWhoami
	cloudWhoami = func(_ context.Context, keyPath string) (*api.UserResponse, error) {
		if keyPath == "" {
			asked <- struct{}{}
			return nil, api.AuthorizationError{StatusCode: http.StatusUnauthorized}
		}
		return nil, errors.New("unexpected")
	}
	t.Cleanup(func() { cloudWhoami = old })
	theCloudPool.status()
	<-asked
	var st api.CloudPoolStatus
	for range 200 {
		if st = theCloudPool.status(); st.ParallelNow == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.Accounts != 2 || st.ParallelNow != 3 || st.Pool[len(st.Pool)-1].Label != "primary" || st.Pool[len(st.Pool)-1].State != "signed_out" {
		t.Fatalf("status = %+v, want the primary listed signed out and only a serving", st)
	}
	l, err := theCloudPool.acquire(context.Background(), nil)
	if err != nil || l.m.label != "a" {
		t.Fatalf("acquire = %v %v, want a", l, err)
	}
	l.release()
}
