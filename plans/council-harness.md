# Council for harnesses: instructions, adaptive chat, agents and escalation

Status: PROPOSED 2026-09-29 (owner's request). Phase 0 (the integration
guide) built the same day. Phases 1-4 wait on the consultants' answer to the
2026-09-29 follow-up, whose flow modes this plan adopts once they rule.

## Why

The council chat is one model name that any client can talk to
([agentic-council-chat.md](agentic-council-chat.md)). The owner's main use cases are not
a person typing into a chat window. They are:

1. **Lead chat model, adaptive.** The council answers a user, and how much of
   the council a turn uses follows what the user asks for during the chat.
2. **An agent.** A harness (Cerebriline first) runs a tool loop with the
   council as its model.
3. **The escalation path.** A harness runs a cheap agent, and hands the work
   to the council when the agent is stuck.

Today the council decides all of this itself, turn by turn:
- the synthesizer's front turn decides answer or forward;
- the builder decides how the council is built;
- stuck detection decides when to change approach.

A harness already knows most of what those calls work out:
- whether this turn is an escalation;
- what the agent tried and what the checks returned;
- what kind of work it is;
- how many cycles the work deserves.

Each decision the council re-derives costs at least one model call. A wrong
one costs a mode switch. On the medium task that is a front turn, a forward, a
builder call and a re-plan cycle before any work is done. Letting the harness
state what it knows should save many trips, and it keeps the council from
switching modes the harness did not ask for.

Also asked: **instructions for the council and the builder**, set by the model's
owner and by the harness, so the builder shapes the council the way the
integration needs.

## Defining constraints

- Off means off. A request without the new fields behaves exactly as today,
  and a model without a council is untouched.
- The harness's statements are bounded like the builder's own (`validBuild`):
  no role count, model or tool permission changes. Researchers and critics
  still only read, and the synthesizer is still the only member that writes.
- Everything is topic-agnostic. The fields carry the harness's words; the
  runtime adds none about any topic.
- Feature-gated. Each contract gets a name in `/api/xollama` `features`,
  never a version check.
- One contract across `/api/chat` and the OpenAI and Anthropic shims. Where a
  shim has no field for it, the guide says so, as it does for `x_read_only`.

## Phase 0 -- the integration guide (built 2026-09-29)

`docs/xollama/council-integration.mdx`, indexed in `docs/docs.json`. It
covers the contract as it is today, for a harness author:
- detection;
- conversation and session rules;
- streaming and tags;
- `council_chat_state`;
- tools and call ids;
- continue;
- usage;
- placement;
- the three use cases as they can be served today;
- a checklist.

What this plan adds is marked *planned* there, with a link here. Every phase
below updates the guide in the same commit as the code.

## Phase 1 -- instructions for the council and the builder

- **Model level** (`types/xollama/council.go`, `xollama tweak model`):
  - `council.instructions`: text every role reads, after its own prompt;
  - `council.builder.instructions`: guidance the builder reads before it
    writes the build ("how this council should be built": e.g. "prefer one
    check cycle", "the synthesizer writes in the house style").
  - The builder's JSON contract stays the runtime's. The instructions shape
    the choices inside it, never the format.
- **Request level** (`ChatRequest.Council.Instructions`, below): the same two
  slots plus per-role text, from the harness. They are appended after the
  model's own, and each carries its source header, as every council message
  does (11.7).
- Tests: every role reads its instructions once, the builder reads its own,
  and a request without them is byte-identical in what members receive.

## Phase 2 -- the harness directive (`council_directive_v1`)

A new top-level `council` object on `/api/chat`, beside `council_chat_state`:

```json
"council": {
  "mode": "auto",
  "instructions": {"council": "...", "builder": "...", "synthesizer": "..."},
  "build": {"target": "...", "instructions": {"researcher": "..."},
            "think": {"researcher": 2048}, "max_tests": 3, "max_steps": 12},
  "evidence": [{"tried": "...", "check": "...", "result": "..."}]
}
```

- **`mode`:**
  - `auto`: today's behaviour, with Phase 3's adaptivity.
  - `answer`: the synthesizer's front turn alone, with no `council_forward`
    offered. A plain agent turn, at plain cost, with the council's state
    kept warm.
  - `escalate`: no front turn. The planner starts at once, and `evidence`
    becomes the turn's prior failed checks, the section the planner already
    reads as "CHECKS THAT FAILED BEFORE THIS COUNCIL". The harness's agent
    already tried; the council does not repeat its reads.
  - `deliberate`: the full council on a turn without an oracle (analysis,
    design, writing).
  - **The mode is the harness's.** The council never switches out of a stated
    mode. Inside `escalate`, the consultants' Escalation / Stream-and-Sift
    transitions apply once they are adopted.
- **`build`:** a harness-supplied build, in the builder's own schema and
  bounds.
  - When present and valid, the builder is not called. That saves a builder
    call and, on a tool turn, the forward round that precedes it.
  - It is kept in the state like the builder's.
  - An invalid one is refused field by field in the done chunk's
    `council_notes`, and the builder runs instead. It is never a 400.
- **`evidence`:** what the harness's agent tried and what came back, a list
  the harness writes in its own words. It is capped in size, like findings.
- A request with `council` and no `council_chat_state` gets the state anyway:
  a directive implies a harness that resumes.
- Tests: each mode's call sequence against the stub model (answer = 1 call
  and no forward tool; escalate = no front and the evidence in the planner's
  prompt; a stated build = no builder call), and the off path.
- Measure on medium and simple (eleven2go), n >= 3 per arm: plain, `auto`,
  `escalate` after a plain agent's failed attempt, and `escalate` with a
  stated build.

## Phase 3 -- adaptive in a chat (`mode: auto`)

What the user says during the chat changes how much of the council a turn
uses, without a setting:
- The route decision (planner, or the synthesizer's front turn) already
  answers direct / continue / council. It gains the user's cues about depth
  and speed ("just a quick answer", "take your time and check it"), read as
  the user's words, in a topic-agnostic instruction.
- A cue holds until the user changes it, and it is kept in the council's
  state.
- A client with its own control (the desktop app's Deliberation toggle,
  Cerebriline's UI) sends `mode` instead. A stated mode wins over a cue.
- Tests: cue phrases move the route both ways, and a stated mode is never
  overridden.

## Phase 4 -- Cerebriline (the ollama session integrates it)

The owner will have the ollama session build it. It covers all three uses:
- **lead model:** chat with a council model, adaptive;
- **agent:** `mode: answer` or `auto`, with tools, `x_read_only` marks and
  the state loop;
- **escalation path:** a cheap agent's turns, then `mode: escalate` with its
  evidence and a stated `build`.

xollama's part:
- the guide;
- a mail with the contract and feature names;
- answering the integration's questions;
- reading back the quality feedback into this plan.

The measure is quality and trips per task against plain, per use case, from
Cerebriline's own runs.

## Open decisions (owner)

1. The field name: `council` (proposed), or `council_directive`.
2. Whether `answer` mode still lets the synthesizer ask for the council
   (a `council_forward` the harness can see as a signal rather than a
   hidden hand-off). Proposed: no, the harness decides.
3. Whether a harness `build` may shorten a model owner's `max_tests`.
   Proposed: yes, never lengthen it.
