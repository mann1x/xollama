package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	gguftest "github.com/ollama/ollama/internal/testutil/gguf"
)

// TestDerivePoolKeyIsNotTheSessionID is the distinction the whole feature rests
// on. A session id identifies one conversation and includes its first user
// message; a pool key identifies what conversations have in common and must
// not. Keying pools by session id would give every conversation its own pool,
// which is the situation pooling exists to fix.
func TestDerivePoolKeyIsNotTheSessionID(t *testing.T) {
	system := api.Message{Role: "system", Content: "You are a careful assistant."}
	tools := api.Tools{{Function: api.ToolFunction{Name: "search"}}}

	a := []api.Message{system, {Role: "user", Content: "what is the capital of France?"}}
	b := []api.Message{system, {Role: "user", Content: "write me a haiku about bees"}}

	keyA := DerivePoolKey("digest", a, tools)
	keyB := DerivePoolKey("digest", b, tools)
	if keyA == "" {
		t.Fatal("no pool key for a conversation with a system prompt and tools")
	}
	if keyA != keyB {
		t.Error("two conversations sharing a system prompt must share a pool key")
	}

	if DeriveSessionID("digest", a, tools) == DeriveSessionID("digest", b, tools) {
		t.Error("the fixture is wrong: these must be different sessions")
	}
}

func TestDerivePoolKey(t *testing.T) {
	system := api.Message{Role: "system", Content: "be brief"}
	user := api.Message{Role: "user", Content: "hello"}

	t.Run("nothing shared, no pool", func(t *testing.T) {
		// A pool over an empty prefix reserves a sequence to save nothing.
		if got := DerivePoolKey("digest", []api.Message{user}, nil); got != "" {
			t.Errorf("DerivePoolKey() = %q, want empty for a conversation with no shared prefix", got)
		}
	})

	t.Run("a different system prompt is a different pool", func(t *testing.T) {
		a := DerivePoolKey("digest", []api.Message{system, user}, nil)
		b := DerivePoolKey("digest", []api.Message{{Role: "system", Content: "be verbose"}, user}, nil)
		if a == b {
			t.Error("different system prompts must not share a pool")
		}
	})

	t.Run("a different model is a different pool", func(t *testing.T) {
		a := DerivePoolKey("digest-a", []api.Message{system, user}, nil)
		b := DerivePoolKey("digest-b", []api.Message{system, user}, nil)
		if a == b {
			t.Error("a pool belongs to the model whose cells hold it")
		}
	})

	t.Run("tool order does not split a pool", func(t *testing.T) {
		// A JSON round trip can reorder them, and a reordered list is the same
		// prefix as far as anyone typing it is concerned.
		a := DerivePoolKey("digest", []api.Message{system, user}, api.Tools{
			{Function: api.ToolFunction{Name: "search"}},
			{Function: api.ToolFunction{Name: "fetch"}},
		})
		b := DerivePoolKey("digest", []api.Message{system, user}, api.Tools{
			{Function: api.ToolFunction{Name: "fetch"}},
			{Function: api.ToolFunction{Name: "search"}},
		})
		if a != b {
			t.Error("reordering the tool list must not split one pool into two")
		}
	})

	t.Run("tools alone are enough to pool", func(t *testing.T) {
		if got := DerivePoolKey("digest", []api.Message{user}, api.Tools{
			{Function: api.ToolFunction{Name: "search"}},
		}); got == "" {
			t.Error("a shared tool definition is a shared prefix worth pooling")
		}
	})
}

func TestPoolRegistry(t *testing.T) {
	// claimOrFail takes the seat and asserts nothing had to be released for it.
	claimOrFail := func(t *testing.T, r *poolRegistry, key string) {
		t.Helper()
		release, ok := r.claim(key)
		if !ok {
			t.Fatalf("claim(%q) refused", key)
		}
		if len(release) != 0 {
			t.Fatalf("claim(%q) wanted %v released with room to spare", key, release)
		}
	}
	// add is claim + remember, the whole successful creation.
	add := func(t *testing.T, r *poolRegistry, key string, id int) {
		t.Helper()
		claimOrFail(t, r, key)
		r.remember(key, id)
	}

	t.Run("remembers and finds", func(t *testing.T) {
		r := newPoolRegistry(2)
		add(t, r, "a", 7)
		if id, ok := r.lookup("a"); !ok || id != 7 {
			t.Errorf("lookup = %d, %v, want 7, true", id, ok)
		}
		if _, ok := r.lookup("b"); ok {
			t.Error("found a pool that was never created")
		}
	})

	t.Run("the least recently used is released before the create needing its seat", func(t *testing.T) {
		// The bound is the number of seats the engine was given, and a create
		// past the last seat is refused rather than queued. So the release has
		// to come out of claim, before the create -- if it came out of remember
		// it would run only after a create that, at the bound, cannot succeed.
		r := newPoolRegistry(2)
		add(t, r, "a", 1)
		add(t, r, "b", 2)
		r.lookup("a") // a is now the more recent of the two

		release, ok := r.claim("c")
		if !ok {
			t.Fatal("claim refused at the bound instead of making room")
		}
		if !slices.Equal(release, []int{2}) {
			t.Fatalf("claim asked to release %v, want [2] — b was the least recently used", release)
		}
		if _, ok := r.lookup("b"); ok {
			t.Error("the released pool is still in the registry")
		}

		r.remember("c", 3)
		for _, k := range []string{"a", "c"} {
			if _, ok := r.lookup(k); !ok {
				t.Errorf("%q should have survived", k)
			}
		}
	})

	t.Run("in-flight creations hold seats too", func(t *testing.T) {
		// A creation that has not returned yet has already taken a seat on the
		// engine. Counting only live pools would let two creations race for one
		// seat and lose.
		r := newPoolRegistry(2)
		add(t, r, "a", 1)
		claimOrFail(t, r, "b") // in flight, not yet remembered

		release, ok := r.claim("c")
		if !ok {
			t.Fatal("claim refused")
		}
		if !slices.Equal(release, []int{1}) {
			t.Fatalf("claim asked to release %v, want [1] — the in-flight claim on b occupies the other seat", release)
		}
	})

	t.Run("a claim is refused when only in-flight creations hold the seats", func(t *testing.T) {
		// There is nothing safe to evict here: releasing another request's
		// pool-to-be would move the failure rather than fix it.
		r := newPoolRegistry(1)
		claimOrFail(t, r, "a")
		if _, ok := r.claim("b"); ok {
			t.Error("granted a seat that only an in-flight creation could have given up")
		}
		// and the refusal must not have left b marked as creating
		r.abandon("a")
		claimOrFail(t, r, "b")
	})

	t.Run("one claim wins", func(t *testing.T) {
		// A burst of first requests for one prefix must not spend several pool
		// seats on the same prefix.
		r := newPoolRegistry(4)

		var wins int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, ok := r.claim("a"); ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if wins != 1 {
			t.Errorf("%d goroutines claimed the same prefix, want 1", wins)
		}
	})

	t.Run("a claim that fails can be retried", func(t *testing.T) {
		r := newPoolRegistry(1)
		claimOrFail(t, r, "a")
		if _, ok := r.claim("a"); ok {
			t.Fatal("second claim granted while the first was in flight")
		}
		r.abandon("a")
		claimOrFail(t, r, "a")
	})

	t.Run("an existing pool is not re-claimed", func(t *testing.T) {
		r := newPoolRegistry(2)
		add(t, r, "a", 1)
		if _, ok := r.claim("a"); ok {
			t.Error("claimed a prefix that already has a pool")
		}
	})

	t.Run("drain empties and reports", func(t *testing.T) {
		r := newPoolRegistry(4)
		add(t, r, "a", 3)
		add(t, r, "b", 1)

		if got := r.drain(); !slices.Equal(got, []int{1, 3}) {
			t.Errorf("drain = %v, want [1 3]", got)
		}
		if got := r.drain(); len(got) != 0 {
			t.Errorf("second drain = %v, want nothing left", got)
		}
	})

	t.Run("a nil registry is inert", func(t *testing.T) {
		// Pooling off is the common case; every call has to tolerate it.
		var r *poolRegistry
		if _, ok := r.lookup("a"); ok {
			t.Error("a nil registry found something")
		}
		if _, ok := r.claim("a"); ok {
			t.Error("a nil registry granted a claim")
		}
		r.remember("a", 1)
		r.abandon("a")
		if got := r.drain(); got != nil {
			t.Errorf("drain on a nil registry = %v", got)
		}
	})

	t.Run("an empty key is never pooled", func(t *testing.T) {
		r := newPoolRegistry(2)
		if _, ok := r.claim(""); ok {
			t.Error("claimed the empty prefix")
		}
		if _, ok := r.lookup(""); ok {
			t.Error("found a pool for the empty prefix")
		}
	})
}

// poolStub is a stand-in engine that records what was asked of it.
type poolStub struct {
	mu      sync.Mutex
	paths   []string
	bodies  []string
	status  int
	payload string
	// tokenPayload answers /tokenize, which the pool path calls before it
	// creates anything.
	tokenPayload string
}

func (p *poolStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		p.mu.Lock()
		p.paths = append(p.paths, r.Method+" "+r.URL.Path)
		p.bodies = append(p.bodies, string(body))
		status, payload := p.status, p.payload
		if r.URL.Path == "/tokenize" {
			status, payload = 0, p.tokenPayload
		}
		p.mu.Unlock()

		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}
}

func poolRunner(t *testing.T, stub *poolStub) *llamaServerRunner {
	t.Helper()

	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return &llamaServerRunner{port: port, usedOpencoti: true, pools: newPoolRegistry(2)}
}

func TestCreatePoolFromTokens(t *testing.T) {
	stub := &poolStub{payload: `{"pool_id":4,"seq_id":9,"prefix_len":812}`}
	s := poolRunner(t, stub)

	id, err := s.createPoolFromTokens(t.Context(), []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if id != 4 {
		t.Errorf("pool id = %d, want 4", id)
	}
	if got := stub.paths[0]; got != "POST /polykv/pools" {
		t.Errorf("called %q, want POST /polykv/pools", got)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}

	// The tokens form, not from_session. A session pool holds the whole
	// conversation, most of which can never be shared -- and on a
	// sliding-window model the cells below its window become reclaimable once
	// the session moves on, so a later attach can land over a hole.
	if _, ok := sent["from_session"]; ok {
		t.Error("created from a session; the pool must be the shared prefix itself")
	}
	tokens, ok := sent["tokens"].([]any)
	if !ok || len(tokens) != 3 {
		t.Fatalf("tokens = %v, want the three that were passed", sent["tokens"])
	}

	// A swept pool whose id xollama still holds is not an error -- it is a
	// silent full reprocess -- so the lifetime has to be ours.
	if sent["pin"] != true {
		t.Error("the pool must be pinned, or the engine's idle sweep can drop it while we still hold its id")
	}
	if sent["ephemeral"] != false {
		t.Error("ephemeral must be cleared explicitly; pinned wins either way, but the engine's JSON should read true")
	}
}

func TestCreatePoolFromTokensRefused(t *testing.T) {
	stub := &poolStub{status: http.StatusConflict, payload: `{"error":"no free pool seq-ids"}`}
	s := poolRunner(t, stub)

	if _, err := s.createPoolFromTokens(t.Context(), []int{1, 2}); err == nil {
		t.Fatal("a refused creation must be an error, not a pool id of zero")
	}
}

func TestCreatePoolFromTokensNeedsTokens(t *testing.T) {
	stub := &poolStub{payload: `{"pool_id":1}`}
	s := poolRunner(t, stub)

	if _, err := s.createPoolFromTokens(t.Context(), nil); err == nil {
		t.Error("a pool over nothing is not a pool")
	}
	if len(stub.paths) != 0 {
		t.Errorf("asked the engine anyway: %v", stub.paths)
	}
}

// TestTokenizePromptMatchesTheServingPath pins the one option that decides
// whether a pool shares anything at all. The engine tokenises a prompt it is
// about to run with add_special AND parse_special true; /tokenize defaults
// add_special to false. Leaving it out drops the leading token on any model
// whose tokeniser adds one, and a pool whose first token differs from the
// request's matches at zero.
func TestTokenizePromptMatchesTheServingPath(t *testing.T) {
	stub := &poolStub{tokenPayload: `{"tokens":[9,8,7]}`}
	s := poolRunner(t, stub)

	tokens, err := s.tokenizePrompt(t.Context(), "<|im_start|>system")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tokens, []int{9, 8, 7}) {
		t.Errorf("tokens = %v, want [9 8 7]", tokens)
	}
	if got := stub.paths[0]; got != "POST /tokenize" {
		t.Errorf("called %q, want POST /tokenize", got)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["add_special"] != true {
		t.Error("add_special must be true: /tokenize defaults it to false, the serving path does not")
	}
	if sent["parse_special"] != true {
		t.Error("parse_special must be true, or the template's own markup tokenises as plain text")
	}
}

func TestCommonTokenPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []int
		want []int
	}{
		{"shared opening", []int{1, 2, 3, 9}, []int{1, 2, 3, 4, 5}, []int{1, 2, 3}},
		{"nothing in common", []int{7}, []int{8}, []int{}},
		{"one contains the other", []int{1, 2}, []int{1, 2, 3}, []int{1, 2}},
		{"identical", []int{1, 2}, []int{1, 2}, []int{1, 2}},
		{"empty", nil, []int{1}, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commonTokenPrefix(tc.a, tc.b); !slices.Equal(got, tc.want) {
				t.Errorf("commonTokenPrefix(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestOfferWaitsForASecondConversation is the behaviour the redesign turns on.
// One conversation is not evidence of a shared anything, and a pool for a
// prefix that never recurs is a reserved sequence spent on nobody.
func TestOfferWaitsForASecondConversation(t *testing.T) {
	long := make([]int, minPoolPrefixTokens+50)
	for i := range long {
		long[i] = i
	}
	diverged := slices.Clone(long)
	diverged[minPoolPrefixTokens+10] = -1

	r := newPoolRegistry(2)
	if got := r.offer("k", long); got != nil {
		t.Fatalf("first sighting produced a pool of %d tokens; it has nothing to compare against", len(got))
	}

	got := r.offer("k", diverged)
	if len(got) != minPoolPrefixTokens+10 {
		t.Fatalf("shared prefix = %d tokens, want %d — the run the two agree on", len(got), minPoolPrefixTokens+10)
	}
}

func TestOfferIgnoresPrefixesTooShortToPayForASeat(t *testing.T) {
	short := make([]int, minPoolPrefixTokens-1)
	r := newPoolRegistry(2)

	r.offer("k", short)
	if got := r.offer("k", short); got != nil {
		t.Errorf("pooled %d tokens; a seat costs a sequence and does not repay that", len(got))
	}
}

func TestOfferIsBounded(t *testing.T) {
	long := make([]int, maxPoolPrefixTokens+500)
	r := newPoolRegistry(2)

	r.offer("k", long)
	got := r.offer("k", long)
	if len(got) != maxPoolPrefixTokens {
		t.Errorf("shared prefix = %d tokens, want it capped at %d", len(got), maxPoolPrefixTokens)
	}

	// and the first-sighting map must not grow without bound either
	r2 := newPoolRegistry(2)
	for i := range maxPendingPrefixes + 20 {
		r2.offer(fmt.Sprintf("key-%d", i), []int{i})
	}
	r2.mu.Lock()
	pending := len(r2.pending)
	r2.mu.Unlock()
	if pending > maxPendingPrefixes {
		t.Errorf("holding %d first-sightings, want at most %d", pending, maxPendingPrefixes)
	}
}

func TestOfferStopsOncePooled(t *testing.T) {
	long := make([]int, minPoolPrefixTokens+10)
	r := newPoolRegistry(2)
	if _, ok := r.claim("k"); !ok {
		t.Fatal("claim refused")
	}
	r.remember("k", 1)

	r.offer("k", long)
	if got := r.offer("k", long); got != nil {
		t.Error("kept working towards a pool this prefix already has")
	}
}

func TestReleasePool(t *testing.T) {
	stub := &poolStub{}
	s := poolRunner(t, stub)

	if err := s.releasePool(t.Context(), 4); err != nil {
		t.Fatal(err)
	}
	// Release is a POST. The engine has no DELETE route at all.
	if got := stub.paths[0]; got != "POST /polykv/pools/4/release" {
		t.Errorf("called %q, want POST /polykv/pools/4/release", got)
	}
}

// TestReleasePoolsAtShutdown covers the other half of pinning: what we pin, we
// must release, or a restarted model leaves cells behind.
func TestReleasePoolsAtShutdown(t *testing.T) {
	stub := &poolStub{}
	s := poolRunner(t, stub)
	s.pools.remember("a", 1)
	s.pools.remember("b", 2)

	s.releasePools()

	if len(stub.paths) != 2 {
		t.Fatalf("released %d pools, want 2: %v", len(stub.paths), stub.paths)
	}
	for _, want := range []string{"POST /polykv/pools/1/release", "POST /polykv/pools/2/release"} {
		if !slices.Contains(stub.paths, want) {
			t.Errorf("missing %q from %v", want, stub.paths)
		}
	}
}

// TestPoolForIsInertWithoutPooling is the off path: a runner with no registry
// attaches nothing, which is what every stock and unpooled load must do.
func TestPoolForIsInertWithoutPooling(t *testing.T) {
	s := &llamaServerRunner{}
	if got := s.poolFor("some-key"); got != nil {
		t.Errorf("poolFor = %d, want no pool with pooling off", *got)
	}
}

// TestCapturePoolIsGatedOnTheEngine keeps pool creation off stock llama.cpp,
// which has no pool registry to create anything in.
func TestCapturePoolIsGatedOnTheEngine(t *testing.T) {
	stub := &poolStub{payload: `{"pool_id":1}`}
	s := poolRunner(t, stub)
	s.usedOpencoti = false

	s.capturePool("key", poolSource{prompt: "a rendered prompt"})

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.paths) != 0 {
		t.Errorf("reached a stock engine: %v", stub.paths)
	}
}

func TestCapturePoolNeedsAKeyAndSomethingToRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		src  poolSource
	}{
		{"no key", "", poolSource{prompt: "rendered"}},
		{"nothing to render", "key", poolSource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &poolStub{payload: `{"pool_id":1}`}
			s := poolRunner(t, stub)

			s.capturePool(tc.key, tc.src)

			stub.mu.Lock()
			defer stub.mu.Unlock()
			if len(stub.paths) != 0 {
				t.Errorf("went to the engine anyway: %v", stub.paths)
			}
		})
	}
}

// TestPoolCreateResponseIgnoresTheRest proves nothing here depends on the shape
// of the engine's pool JSON beyond the one field it needs.
func TestPoolCreateResponseIgnoresTheRest(t *testing.T) {
	var res poolCreateResponse
	body := `{"pool_id":11,"seq_id":3,"parent":-1,"branch_pos":0,"own_len":812,"children":[],` +
		`"prefix_len":812,"prefix_hash":"deadbeef","pinned":true,"ephemeral":false,` +
		`"attached_ever":false,"created_ts":1,"last_access_ts":1,"source_session":"xo-a","orphaned_pin":false}`
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.PoolID != 11 {
		t.Errorf("pool_id = %d, want 11", res.PoolID)
	}
	if strings.Contains(body, "pool_id") && res.PoolID == 0 {
		t.Error("the one field that matters was not read")
	}
}

// TestCapturePoolReleasesBeforeCreating pins the ordering the engine forces on
// us: at the pool bound it refuses a create outright rather than making room,
// so the release for the seat has to reach it FIRST. Asserting on the order of
// the calls rather than the set of them is the whole point of the test.
func TestCapturePoolReleasesBeforeCreating(t *testing.T) {
	prefix := make([]int, minPoolPrefixTokens+10)
	for i := range prefix {
		prefix[i] = i
	}
	payload, err := json.Marshal(map[string]any{"tokens": prefix})
	if err != nil {
		t.Fatal(err)
	}

	stub := &poolStub{payload: `{"pool_id":9}`, tokenPayload: string(payload)}
	s := poolRunner(t, stub) // registry bound is 2

	// Fill both seats, so the third prefix can only be pooled by giving one up.
	for key, id := range map[string]int{"a": 1, "b": 2} {
		if _, ok := s.pools.claim(key); !ok {
			t.Fatalf("claim(%q) refused", key)
		}
		s.pools.remember(key, id)
	}
	s.pools.lookup("b") // a is now the least recently used

	// Two conversations: the first is only remembered, the second is what the
	// pool is built from.
	s.capturePool("c", poolSource{prompt: "rendered one"})
	waitForPoolCalls(t, stub, 1)
	s.capturePool("c", poolSource{prompt: "rendered two"})

	paths := polykvCalls(t, stub, 2)
	want := []string{"POST /polykv/pools/1/release", "POST /polykv/pools"}
	if !slices.Equal(paths, want) {
		t.Errorf("engine saw\n  %v\nwant\n  %v", paths, want)
	}
}

// polykvCalls waits for n pool-registry calls and returns them, ignoring the
// tokenize calls that precede them.
func polykvCalls(t *testing.T, stub *poolStub, n int) []string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		stub.mu.Lock()
		var pool []string
		for _, p := range stub.paths {
			if strings.Contains(p, "/polykv/") {
				pool = append(pool, p)
			}
		}
		all := slices.Clone(stub.paths)
		stub.mu.Unlock()

		if len(pool) >= n {
			return pool
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine saw only %v, want %d pool calls", all, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForPoolCalls waits for the background capture goroutine to finish its
// work, since capturePool deliberately does not block the request that
// triggered it.
func waitForPoolCalls(t *testing.T, stub *poolStub, n int) []string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		stub.mu.Lock()
		paths := slices.Clone(stub.paths)
		stub.mu.Unlock()

		if len(paths) >= n {
			return paths
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine saw only %v, want %d calls", paths, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestModelKeepsRecurrentState(t *testing.T) {
	// A pool is a prefix of a per-token cache. A model whose state is a rolling
	// summary has no such prefix to share, and pooling it wastes a seat while
	// looking like it works -- so these have to be recognised before a pool is
	// ever created.
	for _, tc := range []struct {
		name string
		kv   gguftest.KV
		want bool
	}{
		{
			name: "plain attention",
			kv:   gguftest.KV{"general.architecture": "llama", "llama.block_count": uint32(4)},
			want: false,
		},
		{
			name: "an SSM announces itself structurally",
			kv:   gguftest.KV{"general.architecture": "mamba2", "mamba2.ssm.state_size": uint32(128)},
			want: true,
		},
		{
			name: "RWKV time-mix",
			kv:   gguftest.KV{"general.architecture": "rwkv7", "rwkv7.wkv.head_size": uint32(64)},
			want: true,
		},
		{
			name: "a short convolution counts too",
			kv:   gguftest.KV{"general.architecture": "lfm2", "lfm2.shortconv.l_cache": uint32(3)},
			want: true,
		},
		{
			// The braces: an architecture newer than the list, recognised only
			// because it announces its state the way every SSM does. Without
			// this the list would have to be right about models that did not
			// exist when it was written.
			name: "a hybrid too new for the list",
			kv: gguftest.KV{
				"general.architecture":              "someneweng3hybrid",
				"someneweng3hybrid.ssm.conv_kernel": uint32(4),
			},
			want: true,
		},
		{
			// The belt: a hybrid that carries none of the keys above is still
			// recurrent, and llama.cpp classifies it by name alone.
			name: "a hybrid known only by name",
			kv:   gguftest.KV{"general.architecture": "falcon-h1", "falcon-h1.block_count": uint32(4)},
			want: true,
		},
		{
			// Hyphenation is not cosmetic: the GGUF string and the llama.cpp
			// enum disagree, and matching the enum would miss the model.
			name: "a hyphenated architecture string",
			kv:   gguftest.KV{"general.architecture": "minimax-01", "minimax-01.block_count": uint32(4)},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelKeepsRecurrentState(loadTestGGUF(t, tc.kv)); got != tc.want {
				t.Errorf("modelKeepsRecurrentState = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("no model at all", func(t *testing.T) {
		if modelKeepsRecurrentState(nil) {
			t.Error("a model that could not be read is not a reason to refuse pooling")
		}
	})
}

// TestPoolSeatsAreReservedRegardlessOfArchitecture pins the seat cost. It used
// to assert the opposite: a recurrent model got zero seats, because a pool
// snapshotted from a session could never be matched by one and the memory was
// spent on nothing. Both reasons are gone -- the pool is built from the prefix
// tokens and stops at the measured template -- so these models are pooled like
// any other, and the seats they reserve are seats they can use.
func TestPoolSeatsAreReservedRegardlessOfArchitecture(t *testing.T) {
	cfg := LlamaServerConfig{}
	t.Setenv("XOLLAMA_SESSION_POOL", "1")
	t.Setenv("XOLLAMA_POLYKV_MAX_POOLS", "2")

	if got := resolvePoolCount(cfg); got != 2 {
		t.Errorf("resolvePoolCount() = %d, want 2", got)
	}
}

// TestPoolZeroAttaches is the bug that made the engine's FIRST pool useless.
// Pool ids are handed out from zero, so "attached" cannot be spelled as a
// non-zero int -- and it was, in two places at once: a `pool > 0` guard and an
// `omitempty` on the wire field. Pool 0 was created, pinned and never once
// attached to, which looks exactly like a pool that is simply not being used.
func TestPoolZeroAttaches(t *testing.T) {
	r := newPoolRegistry(2)
	if _, ok := r.claim("k"); !ok {
		t.Fatal("claim refused")
	}
	r.remember("k", 0)

	s := &llamaServerRunner{usedOpencoti: true, pools: r}
	got := s.poolFor("k")
	if got == nil {
		t.Fatal("poolFor found nothing; pool 0 is a real pool")
	}
	if *got != 0 {
		t.Fatalf("poolFor = %d, want 0", *got)
	}

	// and it has to survive all the way onto the wire
	var lsReq llamaServerCompletionRequest
	cfg := LlamaServerConfig{}
	t.Setenv("XOLLAMA_SESSION_AFFINITY", "1")
	t.Setenv("XOLLAMA_SESSION_POOL", "1")
	applySession(&lsReq, true, cfg, "xo-abc", got)

	body, err := json.Marshal(lsReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"pool_id":0`) {
		t.Errorf("pool 0 did not reach the wire: %s", body)
	}
}
