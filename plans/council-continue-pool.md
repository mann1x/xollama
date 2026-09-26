# Council: continue the conversation's pool

**Status:** WAITING — for an opencoti build with patch 0406 on the HF dev repo · **Index:** [MASTER_PLAN](MASTER_PLAN.md)

## Why

A council turn today rebuilds its conversation layer (P1) from the rendered
prompt: `server/council_polykv.go` renders the conversation up to
`councilSentinel`, creates a pool from those bytes, and the engine prefills
whatever its prefix cache does not already hold. The pools are released at
the turn's end, so the next turn starts from the owner session's cache again.

opencoti patch 0406 (dev build b124 `2609260932001`, `3d1f417396`; mail #343)
adds two things that could make that cheaper:

- **`continue_pool`**: `"continue_pool": true` + `"pool_id"` on
  `/completion`, `/v1/chat/completions` and `/apply-template`. The prompt is
  the pool's stored token ids followed by the tokenized suffix, so the engine
  reuses the whole pool (`cache_n == len(pool)`) and never re-tokenizes it.
  For chat, pass only the new messages. The stored stream must end at the end
  of an assistant reply, and multimodal input is refused.
- **Unowned pools**: `"unowned": true` on `POST /polykv/pools` and the fork
  route. The pool has no owner, is charged to the base, and each attacher
  books its own `num_ctx`; combining it with `session_id` is a 400.

Features: `pool_unowned_v1`, `pool_continue_v1`. Both are additive: existing
requests are unchanged, and `/api/engine` already forwards them, since they are
fields on routes it proxies.

## Trigger

Start when a build carrying 0406 is **published to the HF dev repo** (the
owner's call, 2026-09-26). Until then b124 is a local dev build and xollama
does not target it. The engine pin still moves only on a measurement
(`scripts/phase2-engine-ab.py`, run as `ollama`).

## Questions for the first phase

- Can the conversation's layer outlive a turn, as a pool kept for the next
  turn, and be continued with the new user message, instead of being rebuilt?
  That needs the stored stream to end at the assistant reply's end, which is
  true after the synthesizer's answer is appended.
- On a recurrent-state (hybrid) model, a kept pool holds one of the engine's
  few state cells between turns (bug-118). Is the saving worth the cell, and
  does the answer change with the context size (#345)?
- Do unowned pools let the stage layers drop the owner session, and does that
  simplify release ordering (leaves first)?
- Gating: `pool_continue_v1` in `/props` features, as `polykv_subpools_v1` is
  today, with the current path unchanged when it is absent.

## Phases

- [ ] Phase 0 — measure on the published build: turn-over-turn prefill with
  and without continuing P1, on a dense model and on the hybrid omnimerge at
  16k and at 131k.
- [ ] Phase 1 — build, behind the feature flag, if Phase 0 shows a saving.
