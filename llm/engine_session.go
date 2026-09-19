package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// xollama-hook: engine-session
//
// Session identity for engines that have it.
//
// opencoti-llamafile can bind a conversation to the slot that already holds its
// KV, and can let several conversations attend one physical copy of a shared
// opening instead of one copy each. Both are per-request fields on the
// completion body; upstream llama.cpp has neither and ignores them, but we
// still send nothing there, because "off means off" is measured against a
// byte-identical request.
//
// See docs/xollama/sessions.mdx for the user-facing description.

// sessionIDPrefix marks a derived identifier, so a value in an engine log is
// recognisably ours rather than something a caller sent.
const sessionIDPrefix = "xo-"

// sessionSettings is what a request resolved to, after the model's own config
// and the environment have been consulted.
type sessionSettings struct {
	Affinity bool
	Pool     bool
}

// resolveSessionSettings applies the precedence the documentation promises:
// the model's xollama.json first, the environment second, the built-in default
// last. The request is not consulted here — a caller that sends a session id is
// asking for a specific identity, not switching the feature on, and a caller
// that sends none must still get the derivation.
//
// Every caller must gate on the engine first: these settings mean nothing on a
// runner that has no session affinity.
func resolveSessionSettings(cfg LlamaServerConfig) sessionSettings {
	out := sessionSettings{
		Affinity: envconfig.SessionAffinity(),
		Pool:     envconfig.SessionPool(),
	}
	if want, stated := cfg.sessionAffinity(); stated {
		out.Affinity = want
	}
	if want, stated := cfg.sessionPool(); stated {
		out.Pool = want
	}
	// Validation refuses pool-without-affinity in a model config, but the
	// environment can still be asked for the same impossible pair. A pool has
	// nothing to attach to without session identity, so affinity wins.
	if !out.Affinity {
		out.Pool = false
	}
	return out
}

// DeriveSessionID names the conversation a chat request belongs to.
//
// The identity has to be stable as the conversation grows and distinct between
// conversations, which rules out hashing the whole prompt — that changes on
// every turn, and a session that changes every turn is no session at all. So it
// hashes the part that does not move: the model, the system messages, the tool
// definitions, and the first user turn.
//
// What this costs, stated plainly because it is the interesting property: two
// conversations that share a model, a system prompt, their tools AND their
// first user message derive the same identity and will contend for one slot.
// That is a lost optimisation, never a wrong answer — the engine still serves
// each request from its own prompt. A caller that hits it sends its own
// session_id.
//
// An empty result means "derive nothing": no system prompt, no tools and no
// user turn is not a conversation worth pinning.
func DeriveSessionID(model string, messages []api.Message, tools api.Tools) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})

	wroteAnything := false
	for _, m := range messages {
		if strings.EqualFold(m.Role, "system") {
			h.Write([]byte(m.Content))
			h.Write([]byte{0})
			wroteAnything = true
		}
	}
	for _, t := range tools {
		h.Write([]byte(t.Function.Name))
		h.Write([]byte{0})
		wroteAnything = true
	}
	for _, m := range messages {
		if strings.EqualFold(m.Role, "user") {
			h.Write([]byte(m.Content))
			h.Write([]byte{0})
			wroteAnything = true
			break
		}
	}
	if !wroteAnything {
		return ""
	}
	return sessionIDPrefix + hex.EncodeToString(h.Sum(nil))[:16]
}

// sessionFieldsFor decides what a request actually carries, given the engine it
// landed on and what the model and the environment asked for.
//
// The engine gate comes first and is absolute: a request served by stock
// llama.cpp carries no trace of any of this, which is what makes an A/B against
// upstream honest.
//
// Affinity being off also drops a session id the CALLER supplied. That is
// deliberate. "Off" is an operator or a publisher saying this model must not
// pin conversations to slots, and a client header is not the place to overrule
// it; a caller who wants affinity asks the server for it.
func sessionFieldsFor(engineHasSessions bool, cfg LlamaServerConfig, sessionID string, poolID int) (string, int) {
	if !engineHasSessions {
		return "", 0
	}
	set := resolveSessionSettings(cfg)
	if !set.Affinity {
		return "", 0
	}
	if !set.Pool {
		poolID = 0
	}
	// Pool ids are handed out by the engine, and zero is a meaningful slot in
	// the legacy field it replaces, so only a positive id means "attached".
	if poolID < 0 {
		poolID = 0
	}
	return sessionID, poolID
}

// applySession fills the session fields of a completion body.
func applySession(lsReq *llamaServerCompletionRequest, engineHasSessions bool, cfg LlamaServerConfig, sessionID string, poolID int) {
	id, pool := sessionFieldsFor(engineHasSessions, cfg, sessionID, poolID)
	if id != "" {
		lsReq.SessionID = id
	}
	if pool > 0 {
		lsReq.PoolID = pool
	}
}
