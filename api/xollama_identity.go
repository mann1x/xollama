package api

import (
	"context"
	"net/url"
)

// The fork's identity endpoint.
//
// xollama listens on its own port (22434) precisely so it can coexist with a
// stock ollama on 11434. That only helps if a client can tell the two apart,
// and nothing upstream distinguishes them: /api/version answers with a bare
// version string on both, and a dev build's is "0.0.0", so it identifies
// neither the fork nor the release.
//
// So the fork states it. A stock ollama answers this route with 404, which is
// the discriminator -- see ResolveHost.
const XollamaIdentityPath = "/api/xollama"

// XollamaIdentity is what XollamaIdentityPath answers.
//
// Xollama is always true when the field is present at all; it exists so a proxy
// that answers every path with 200 and an empty body is not mistaken for the
// fork.
type XollamaIdentity struct {
	Xollama bool   `json:"xollama"`
	Version string `json:"version,omitempty"`
	// Features names what this build serves, so a client gates each piece on
	// its name rather than on a version: a dev build's version is "0.0.0".
	// A name is added when its feature ships and never changes meaning; a
	// changed contract is a new name (…_v2).
	Features []string `json:"features,omitempty"`
}

// The names XollamaIdentity.Features carries.
const (
	// FeatureCouncil: a model can be a council (plans/agentic-council-chat.md).
	FeatureCouncil = "council"
	// FeatureCouncilCompaction: a council compacts its own conversation, so
	// a client sends the history as the user sees it and does not compact it.
	FeatureCouncilCompaction = "council_compaction_v1"
	// FeatureCouncilTags: every thinking chunk of a council turn carries
	// ChatResponse.Council, naming the one member it holds. Content chunks,
	// the answer, carry none.
	FeatureCouncilTags = "council_tags_v1"
	// FeatureClientPlacement: ChatRequest.Placement is honoured -- passed to
	// the engine on a plain turn, and the pool a council turn's root forks
	// from when the conversation starts with it.
	FeatureClientPlacement = "client_placement_v1"
)

// IsXollama reports whether the server at base is this fork, by the identity
// route and, for builds older than it, by the fork's name in /api/version --
// the same answer ResolveHost acts on. Nothing answering is not xollama.
func IsXollama(ctx context.Context, base *url.URL) bool {
	_, x := probeHost(ctx, base)
	return x
}
