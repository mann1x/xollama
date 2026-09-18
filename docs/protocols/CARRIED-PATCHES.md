# Carried patches

Every change xollama carries that is *also* an open PR against upstream
ollama. This file is the contract: a patch is here because upstream has not
taken it, and it leaves when upstream does.

**Rules**

1. Each carried patch is merged onto `main` as its **own `--no-ff` merge**,
   from a branch named after it. Retiring one is then a single revert of an
   identifiable commit range, not surgery.
2. The upstream PR stays open and stays the source of truth. Fixes go to the
   PR branch in `mann1x/ollama` first, then flow here.
3. On every upstream sync, re-check the `state` column. A patch whose PR
   merged upstream is **retired the same day** — carrying it twice is how a
   silent double-apply happens.
4. A patch with no upstream PR is an xollama feature, not a carried patch.
   It belongs in `docs/features/`.

Status as of **2026-09-18**. All twelve are OPEN against `ollama/ollama`.

## Tier 1 — the reason this fork exists

| PR | Title | Branch | Opened | Age |
|---|---|---|---|---|
| [#17566](https://github.com/ollama/ollama/pull/17566) | `api`: bound thinking with a token budget, per request or per model | `up-think-budget` | 2026-08-04 | 45d |
| _(none yet)_ | `/api/tokenize` + `/api/detokenize` — see "Tokenizer endpoints" below | — | — | — |

**#17566 (think budget)** is the headline. Without it there is no way to cap a
reasoning model's thinking, which on a long agentic run is the difference
between a 55-second turn and one that grinds to the token cap. Related:
[#18212](https://github.com/ollama/ollama/pull/18212) ends a spent budget at a
line boundary rather than mid-word.

## Tier 2 — correctness fixes

| PR | Title | Branch | Opened | Age |
|---|---|---|---|---|
| [#18307](https://github.com/ollama/ollama/pull/18307) | `gemma4`: a tool call can open before the thinking channel is closed | `gemma4-toolcall-in-thinking` | 2026-09-08 | 10d |
| [#18289](https://github.com/ollama/ollama/pull/18289) | `server`: reload when two tags share a blob but need different runner flags | `up-jinja-runner-reuse` | 2026-09-07 | 11d |
| [#18288](https://github.com/ollama/ollama/pull/18288) | `parsers`: drop an unmatched thinking close tag instead of leaking it | `up-gemma4-stray-closer` | 2026-09-07 | 11d |
| [#18281](https://github.com/ollama/ollama/pull/18281) | `llm`: send assistant thinking to the chat template | `up-native-thinking-replay` | 2026-09-07 | 11d |
| [#18212](https://github.com/ollama/ollama/pull/18212) | `llama`: end a spent reasoning budget at a line, not mid-word | `up-reasoning-budget-line-boundary` | 2026-09-03 | 15d |
| [#17914](https://github.com/ollama/ollama/pull/17914) | `qwen3coder`: tolerate a dropped closing tag, stop rewriting parameter values | `qwen3coder-tolerate-malformed-tool-calls` | 2026-08-21 | 28d |
| [#17626](https://github.com/ollama/ollama/pull/17626) | `gemma4`: do not answer with a channel name the parser was cut off from | `up-gemma4-stray-channel-name` | 2026-08-08 | 41d |
| [#17565](https://github.com/ollama/ollama/pull/17565) | `gemma4`: recover a finished tool call that is missing its closing brace | `up-gemma4-object-close` | 2026-08-04 | 45d |
| [#17564](https://github.com/ollama/ollama/pull/17564) | `server`: do not hand over a tool call the model did not finish writing | `up-truncated-tool-calls` | 2026-08-04 | 45d |
| [#17563](https://github.com/ollama/ollama/pull/17563) | `llm`: stop guessing that a repetitive payload is a runaway | `up-repeat-guard` | 2026-08-04 | 45d |

Seven of these are tool-call and thinking-channel parser fixes. They matter
most to agentic clients, which is exactly the workload that exposes them and
the workload upstream's test suite exercises least.

## Tier 3 — platform

| PR | Title | Branch | Opened | Age |
|---|---|---|---|---|
| [#17567](https://github.com/ollama/ollama/pull/17567) | `x/mlxrunner/mlx`: link against libdl on linux | `up-mlx-libdl` | 2026-08-04 | 45d |

## Tokenizer endpoints — not yet carried

ollama exposes no way to tokenize or count tokens against the model actually
loaded. Clients guess, and a guess is wrong by enough to matter when you are
packing a context window. The plumbing is already there: `llm.LlamaServer`
has had `Tokenize(ctx, content)` and `Detokenize(ctx, tokens)` on the
interface for a long time, and llama-server (and opencoti-llamafile) both
serve `/tokenize`. Only the HTTP routes are missing.

Three open upstream PRs address this. They are not equivalent:

| PR | Approach | Size | State | Verdict |
|---|---|---|---|---|
| [#16820](https://github.com/ollama/ollama/pull/16820) | `/api/tokenize` + `/api/detokenize` wired to the existing interface methods via `scheduleRunner` | +160 / 3 files | open 2026-06-19 | **recommended base** |
| [#12030](https://github.com/ollama/ollama/pull/12030) | vocab-only tokenizer loader with its own cache — tokenizes *without* loading the model | +1271 / −354, 14 files | open 2025-08-22, **conflicting** | mine the idea, not the diff |
| [#17478](https://github.com/ollama/ollama/pull/17478) | token *counting* only: `/v1/messages/count_tokens`, `…/input_tokens` | +893 / 18 files | open 2026-07-30, by a maintainer | carry separately, expect it to land |

**Plan.** Take #16820 as the base — it is 160 lines, it reuses machinery that
already exists, and it is the minimum that makes the endpoint real. Then add
#12030's *idea* as an xollama enhancement: a vocab-only path that answers a
token count without pulling a model into VRAM. That is the part agent clients
actually need on a hot loop, and the reason #12030 has thirteen months of
"merge please" comments under it. Do not take #12030's diff — it carries a
README rewrite and two PNGs and no longer merges.

#17478 is by an ollama maintainer and covers the OpenAI/Anthropic
`count_tokens` surface rather than raw tokenization. Carry it if the surface is
wanted, and expect to retire it: maintainer PRs land.
