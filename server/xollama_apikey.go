package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// The local API key (docs/xollama/api-key.mdx): with a key configured, every
// route answers only a request that carries it, as `Authorization: Bearer`
// (OpenAI clients) or `x-api-key` (Anthropic clients). Without one the server
// is open, exactly as upstream.

const (
	// keyRecheck is how long a read of the configured key is trusted before
	// the file is read again, so `xollama tweak server` takes effect without
	// a restart.
	keyRecheck = 2 * time.Second
	// maxKeyFailures within keyFailureWindow from one peer get 429 until the
	// window passes. A generated key is 256 bits and cannot be guessed; this
	// bounds a weak key chosen by hand, and the log noise of a scan.
	maxKeyFailures   = 10
	keyFailureWindow = time.Minute
)

// processToken lets this server call itself (the web search follow-up, the
// OpenAI shim's own requests) without holding the key, of which it keeps
// only a digest. It is random per process and never leaves it: api's
// ClientFromEnvironment sends it only to this server's own address.
var processToken = func() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}()

type apiKeyGate struct {
	mu       sync.Mutex
	digest   [32]byte
	source   string
	checked  time.Time
	failures map[string]*keyFailures
}

type keyFailures struct {
	n     int
	since time.Time
}

var keyGate = &apiKeyGate{failures: map[string]*keyFailures{}}

// current is the configured key, re-read at most every keyRecheck.
func (g *apiKeyGate) current(now time.Time) ([32]byte, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.checked) >= keyRecheck {
		g.digest, g.source = envconfig.ServerAPIKey()
		g.checked = now
	}
	return g.digest, g.source
}

// reload forgets the cached key, after `tweak server` changed it.
func (g *apiKeyGate) reload() {
	g.mu.Lock()
	g.checked = time.Time{}
	g.mu.Unlock()
}

// throttled reports whether peer has failed too often, and for how long.
func (g *apiKeyGate) throttled(peer string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.failures[peer]
	if f == nil || now.Sub(f.since) >= keyFailureWindow {
		return 0
	}
	if f.n < maxKeyFailures {
		return 0
	}
	return keyFailureWindow - now.Sub(f.since)
}

func (g *apiKeyGate) fail(peer string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.failures) > 4096 {
		for p, f := range g.failures {
			if now.Sub(f.since) >= keyFailureWindow {
				delete(g.failures, p)
			}
		}
	}
	f := g.failures[peer]
	if f == nil || now.Sub(f.since) >= keyFailureWindow {
		f = &keyFailures{since: now}
		g.failures[peer] = f
	}
	f.n++
}

func (g *apiKeyGate) succeed(peer string) {
	g.mu.Lock()
	delete(g.failures, peer)
	g.mu.Unlock()
}

// presentedKey is the key a request carries: `Authorization: Bearer <key>`,
// else `x-api-key`. Never a query parameter: URLs reach logs and histories.
func presentedKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, v, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// keyExempt is a request the key does not govern: a CORS preflight, which
// carries no credentials by design and is answered before this runs anyway,
// and the Codex desktop proxy, which answers loopback callers only and
// carries Codex's own OpenAI key in Authorization.
func keyExempt(r *http.Request) bool {
	return r.Method == http.MethodOptions || strings.HasPrefix(r.URL.Path, "/api/codex/")
}

// apiKeyMiddleware enforces the local API key on every route. It also
// registers this process's token with api, so the server's calls to itself
// pass.
func (s *Server) apiKeyMiddleware() gin.HandlerFunc {
	api.SetProcessAPIKey(processToken)
	return func(c *gin.Context) {
		now := time.Now()
		digest, source := keyGate.current(now)
		if source == "" || keyExempt(c.Request) {
			c.Next()
			return
		}
		// RemoteIP, never ClientIP: gin trusts X-Forwarded-For from anyone by
		// default, and a spoofed header must not escape the throttle.
		peer := c.RemoteIP()
		if wait := keyGate.throttled(peer, now); wait > 0 {
			c.Header("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests with a wrong API key; try again later"})
			return
		}
		key := presentedKey(c.Request)
		given := envconfig.KeyDigest(key)
		ok := key != "" && subtle.ConstantTimeCompare(given[:], digest[:]) == 1
		if !ok && key != "" {
			ok = subtle.ConstantTimeCompare([]byte(key), []byte(processToken)) == 1
		}
		if !ok {
			// Only a wrong key counts: a request without one is not a guess,
			// and counting it would throttle an honest client behind the same
			// NAT that has not been given the key yet.
			if key != "" {
				keyGate.fail(peer, now)
			}
			c.Header("WWW-Authenticate", `Bearer realm="xollama"`)
			msg := "this xOllama server requires an API key: send it as 'Authorization: Bearer <key>' or 'x-api-key'"
			if key != "" {
				msg = "wrong API key"
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": msg})
			return
		}
		keyGate.succeed(peer)
		// The key has done its job: nothing after this point may forward it
		// (the cloud passthrough copies incoming headers upstream).
		c.Request.Header.Del("Authorization")
		c.Request.Header.Del("x-api-key")
		c.Next()
	}
}

// proxied reports whether a request came through a proxy that said so. Behind
// a TLS proxy on this host every client looks like loopback.
func proxied(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip", "Forwarded", "Via"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// loopbackPeer reports whether the direct peer is this machine.
func loopbackPeer(c *gin.Context) bool {
	ip := net.ParseIP(c.RemoteIP())
	return ip != nil && ip.IsLoopback()
}
