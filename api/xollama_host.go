package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ollama/ollama/envconfig"
)

// Client-side host resolution, with a fallback to a stock ollama's port.
//
// The server half of this is not negotiable and does not change: xollama BINDS
// what envconfig.Host() says, from XOLLAMA_HOST only, defaulting to 22434. See
// .claude/rules/default-port.md. What follows is only about where the CLI
// CONNECTS when the operator has said nothing at all, and it never reaches the
// listen path -- `serve` calls envconfig.Host() directly.
//
// The case it exists for: someone installs xollama beside the ollama they
// depend on, starts it, and runs `xollama list` in a shell that exports no
// XOLLAMA_HOST. Before this, that dialled 22434 and stopped. Now it also looks
// at 11434, because a single-server install is the common one and an operator
// who has only ever run one server should not have to know the port moved.
//
// The one thing it must not do is drive a stock ollama while the user believes
// they are driving xollama. Fork-only commands (`tweak`, the xollama.json
// layer, the launchers) would fail in confusing ways, and worse, a `pull` or a
// `rm` would land in the other server's store. So a stock ollama found on 11434
// is a refusal with its own message, not a silent connection.

// hostProbeTimeout bounds each probe. Both candidates are loopback, where a
// live server answers in single-digit milliseconds and a dead port refuses
// immediately; this only bounds the pathological case of something accepting
// the connection and never answering.
const hostProbeTimeout = 500 * time.Millisecond

// ollamaFallbackPort is the stock ollama port, consulted only as a fallback and
// never as a bind address.
const ollamaFallbackPort = "11434"

// ErrStockOllamaOnFallbackPort is returned when no xollama is reachable and the
// stock port is answering with something that is not xollama.
var ErrStockOllamaOnFallbackPort = errors.New("found a server that is not xollama")

// ResolveHost reports the base URL the CLI should talk to.
//
// An explicit XOLLAMA_HOST always wins outright and is never probed: if the
// operator named a host, a command must fail against THAT host rather than
// quietly succeed somewhere else. Probing only happens when they named nothing.
func ResolveHost(ctx context.Context) (*url.URL, error) {
	configured := envconfig.ConnectableHost()
	explicit := strings.TrimSpace(envconfig.XollamaOnly("OLLAMA_HOST")) != ""

	fallback := *configured
	fallback.Host = joinHostPort(configured.Hostname(), ollamaFallbackPort)

	return resolveHostFrom(ctx, configured, &fallback, explicit)
}

// resolveHostFrom is ResolveHost's decision, with the two candidates handed in
// so a test can point them at servers it controls instead of at 22434 and
// 11434, which it cannot assume are free or even absent on a developer's box.
func resolveHostFrom(ctx context.Context, configured, fallback *url.URL, explicit bool) (*url.URL, error) {
	if explicit {
		return configured, nil
	}

	// The default port first. Anything answering there is taken as ours --
	// including an xollama old enough to predate the identity route, which is
	// why this asks "is something up" rather than "is it xollama".
	if up, _ := probeHost(ctx, configured); up {
		return configured, nil
	}

	up, isXollama := probeHost(ctx, fallback)
	switch {
	case up && isXollama:
		return fallback, nil
	case up:
		return nil, fmt.Errorf(
			"%w on %s, and no xollama is running on %s.\n"+
				"Refusing to use it: a pull, rm or tweak would act on that server's model store, not xollama's.\n"+
				"Start xollama, or set XOLLAMA_HOST=%s to use it on purpose",
			ErrStockOllamaOnFallbackPort, fallback.Host, configured.Host, fallback.Host)
	}

	// Nothing anywhere. Hand back the configured host so the failure the caller
	// reports is the familiar "could not connect" against our own port, rather
	// than a port the operator never mentioned.
	return configured, nil
}

// probeHost reports whether something is listening, and whether it identified
// itself as xollama. A transport error means nothing is there; any HTTP answer
// at all means something is.
func probeHost(ctx context.Context, base *url.URL) (up, isXollama bool) {
	ctx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()

	ident := *base
	ident.Path = strings.TrimRight(base.Path, "/") + XollamaIdentityPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ident.String(), nil)
	if err != nil {
		return false, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var id XollamaIdentity
		if err := json.NewDecoder(resp.Body).Decode(&id); err == nil && id.Xollama {
			return true, true
		}
	}

	// Something answered, but not the identity route. It may still be this
	// fork: every xollama built before that route existed 404s it exactly as a
	// stock ollama does. Measured on this host -- the server on 11434 reported
	// "0.34.2-xollama-12644ae4" and 404'd /api/xollama -- so treating a 404 as
	// proof of a stock ollama would refuse to talk to a real xollama.
	//
	// The version string is what those builds do carry: cmake/local.cmake
	// stamps it at link time and the fork's name is in it. A bare `go build .`
	// stamps nothing and reports "0.0.0", but such a build is necessarily new
	// enough to have the route above.
	return true, versionNamesTheFork(ctx, base)
}

// versionNamesTheFork asks /api/version and reports whether the answer is this
// fork's. Upstream's own version never contains the fork's name.
func versionNamesTheFork(ctx context.Context, base *url.URL) bool {
	v := *base
	v.Path = strings.TrimRight(base.Path, "/") + "/api/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.String(), nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(body.Version), "xollama")
}

// joinHostPort is net.JoinHostPort, bracketing a bare IPv6 literal.
func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
