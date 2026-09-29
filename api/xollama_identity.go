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
	// FeatureCouncilUsage: a council turn's done chunk carries
	// ChatResponse.CouncilUsage, what each role spent.
	FeatureCouncilUsage = "council_usage_v1"
	// FeatureCouncilDirective: ChatRequest.Council is honoured -- a harness
	// states the turn's mode, instructions, build, evidence and check tool
	// (plans/council-harness.md).
	FeatureCouncilDirective = "council_directive_v1"
	// FeatureCouncilTags: every thinking chunk of a council turn carries
	// ChatResponse.Council, naming the one member it holds. Content chunks,
	// the answer, carry none.
	FeatureCouncilTags = "council_tags_v1"
	// FeatureClientPlacement: ChatRequest.Placement is honoured -- passed to
	// the engine on a plain turn, and the pool a council turn's root forks
	// from when the conversation starts with it.
	FeatureClientPlacement = "client_placement_v1"
	// FeatureChatRender: /api/chat with `_debug_render_only` answers
	// `_debug_info.rendered_template`, the exact text the engine would get,
	// generating nothing; on a council model, what its members send. It is
	// the renderer a client cuts its PolyKV prefix from.
	FeatureChatRender = "chat_render_v1"
	// FeatureCouncilChatState: a council turn sends a sealed resume point,
	// council_chat_state, at each checkpoint and on the done chunk, when the
	// request carries the field (empty on a first turn); sent back, it resumes
	// the turn that broke off and restores a lost compaction record.
	FeatureCouncilChatState = "council_chat_state_v1"
	// FeatureCouncilTools: a council turn with tools and council_chat_state
	// uses the tools -- read-only ones (function.x_read_only) for researchers
	// and critics, every one for the synthesizer -- and forwards its members'
	// calls to the client under ids naming the member ("r2:call_x").
	FeatureCouncilTools = "council_tools_v1"
	// FeatureAPIKey: the server can require a local API key
	// (Authorization: Bearer, or x-api-key) on every route; a keyed server
	// answers 401 with WWW-Authenticate: Bearer realm="xollama" without it,
	// this route included.
	FeatureAPIKey = "api_key_v1"
	// FeatureContextWindow: an admitted chat or generate response carries
	// the engine's X-Context-Window (absent: no guaranteed window), and a
	// client stating placement.num_ctx that cannot be booked gets a 429 at
	// once with X-Context-Largest-Admissible and Retry-After, not a wait.
	FeatureContextWindow = "context_window_v1"
)

// IsXollama reports whether the server at base is this fork, by the identity
// route and, for builds older than it, by the fork's name in /api/version --
// the same answer ResolveHost acts on. Nothing answering is not xollama.
func IsXollama(ctx context.Context, base *url.URL) bool {
	_, x := probeHost(ctx, base)
	return x
}
