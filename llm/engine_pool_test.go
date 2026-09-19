package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"
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
	t.Run("remembers and finds", func(t *testing.T) {
		r := newPoolRegistry(2)
		if evict := r.remember("a", 7); len(evict) != 0 {
			t.Errorf("evicted %v with room to spare", evict)
		}
		if id, ok := r.lookup("a"); !ok || id != 7 {
			t.Errorf("lookup = %d, %v, want 7, true", id, ok)
		}
		if _, ok := r.lookup("b"); ok {
			t.Error("found a pool that was never created")
		}
	})

	t.Run("evicts the least recently used at the bound", func(t *testing.T) {
		// The bound is the number of seats the engine was given. Going over it
		// would ask for a pool id the engine has nowhere to put.
		r := newPoolRegistry(2)
		r.remember("a", 1)
		r.remember("b", 2)
		r.lookup("a") // a is now the more recent of the two

		evict := r.remember("c", 3)
		if !slices.Equal(evict, []int{2}) {
			t.Fatalf("evicted %v, want [2] — b was the least recently used", evict)
		}
		if _, ok := r.lookup("b"); ok {
			t.Error("the evicted pool is still in the registry")
		}
		for _, k := range []string{"a", "c"} {
			if _, ok := r.lookup(k); !ok {
				t.Errorf("%q should have survived eviction", k)
			}
		}
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
				if r.claim("a") {
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
		if !r.claim("a") {
			t.Fatal("first claim refused")
		}
		if r.claim("a") {
			t.Fatal("second claim granted while the first was in flight")
		}
		r.abandon("a")
		if !r.claim("a") {
			t.Error("a prefix whose creation failed must be claimable again, or it can never be pooled")
		}
	})

	t.Run("an existing pool is not re-claimed", func(t *testing.T) {
		r := newPoolRegistry(2)
		r.remember("a", 1)
		if r.claim("a") {
			t.Error("claimed a prefix that already has a pool")
		}
	})

	t.Run("drain empties and reports", func(t *testing.T) {
		r := newPoolRegistry(4)
		r.remember("a", 3)
		r.remember("b", 1)

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
		if r.claim("a") {
			t.Error("a nil registry granted a claim")
		}
		if got := r.remember("a", 1); got != nil {
			t.Errorf("remember on a nil registry = %v", got)
		}
		r.abandon("a")
		if got := r.drain(); got != nil {
			t.Errorf("drain on a nil registry = %v", got)
		}
	})

	t.Run("an empty key is never pooled", func(t *testing.T) {
		r := newPoolRegistry(2)
		if r.claim("") {
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
}

func (p *poolStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		p.mu.Lock()
		p.paths = append(p.paths, r.Method+" "+r.URL.Path)
		p.bodies = append(p.bodies, string(body))
		status, payload := p.status, p.payload
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

func TestCreatePoolFromSession(t *testing.T) {
	stub := &poolStub{payload: `{"pool_id":4,"seq_id":9,"prefix_len":812}`}
	s := poolRunner(t, stub)

	id, err := s.createPoolFromSession(t.Context(), "xo-abc123")
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
	if sent["from_session"] != "xo-abc123" {
		t.Errorf("from_session = %v, want the session that just ran", sent["from_session"])
	}
	// Pools created from a session default ephemeral and are swept after a
	// minute of no attaches. A swept pool whose id xollama still holds is not
	// an error — it is a silent full reprocess — so the lifetime has to be ours.
	if sent["pin"] != true {
		t.Error("the pool must be pinned, or the engine's idle sweep can drop it while we still hold its id")
	}
}

func TestCreatePoolFromSessionRefused(t *testing.T) {
	stub := &poolStub{status: http.StatusConflict, payload: `{"error":"no free pool seq-ids"}`}
	s := poolRunner(t, stub)

	if _, err := s.createPoolFromSession(t.Context(), "xo-abc123"); err == nil {
		t.Fatal("a refused creation must be an error, not a pool id of zero")
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
	if got := s.poolFor("some-key"); got != 0 {
		t.Errorf("poolFor = %d, want 0 with pooling off", got)
	}
}

// TestCapturePoolIsGatedOnTheEngine keeps pool creation off stock llama.cpp,
// which has no pool registry to create anything in.
func TestCapturePoolIsGatedOnTheEngine(t *testing.T) {
	stub := &poolStub{payload: `{"pool_id":1}`}
	s := poolRunner(t, stub)
	s.usedOpencoti = false

	s.capturePool("key", "xo-abc")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.paths) != 0 {
		t.Errorf("reached a stock engine: %v", stub.paths)
	}
}

func TestCapturePoolNeedsBothAKeyAndASession(t *testing.T) {
	for _, tc := range []struct{ name, key, session string }{
		{"no key", "", "xo-abc"},
		{"no session", "key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &poolStub{payload: `{"pool_id":1}`}
			s := poolRunner(t, stub)

			s.capturePool(tc.key, tc.session)

			stub.mu.Lock()
			defer stub.mu.Unlock()
			if len(stub.paths) != 0 {
				t.Errorf("created a pool anyway: %v", stub.paths)
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
