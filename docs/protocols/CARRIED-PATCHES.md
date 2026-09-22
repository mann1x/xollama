# Carried patches

Every change xollama carries that is *also* an open PR against upstream
ollama. This file is the contract: a patch is here because upstream has not
taken it, and it leaves when upstream does.

This file says *which* patches are carried and at which merge.
`docs/protocols/FORK-SYNC.md` says *how* they get here — the manifest the fork
publishes, the sha to merge at, and why nothing is ever hand-copied. Read that
first if a patch looks missing or out of date.

**Rules**

1. Each carried patch is merged onto `main` as its **own `--no-ff` merge**,
   from a branch named after it. Retiring one is then a single revert of an
   identifiable commit range, not surgery.
2. The upstream PR stays open and stays the source of truth. Fixes go to the
   PR branch in `mann1x/ollama` first, then flow here.
3. On every upstream sync, re-check the `state` column. A patch whose PR
   merged upstream is **retired the same day** — carrying it twice is how a
   silent double-apply happens.
4. A patch with no upstream PR **and no home in the fork** is an xollama
   feature, not a carried patch. It belongs in `docs/features/`.
5. A patch the fork's manifest marks `fork-only` is a third thing: authored in
   `mann1x/ollama`, carried here, but never reported upstream. Its
   `upstream_pr` is `null`, so the **branch slug is its identifier** — every
   other row in this file keys on the PR number, and these cannot. They are
   listed apart, below.

**Status as of 2026-09-21: all thirteen are carried, and all thirteen are still
OPEN against `ollama/ollama`.** The twelve that have a home in `mann1x/ollama`
are now consumed from its first `PATCHES.json` — `base: v0.34.2`,
`integration.sha 3af5f361` — each re-merged at the manifest's sha, in
`patches[]` order. The fork rebased every branch onto v0.34.2 and force-pushed,
so the 2026-09-18 merges below are of commits that no longer exist there; they
are kept because rule 1 makes retiring a patch the revert of its whole merge
set, and that set is now two entries long for most rows.

| PR | branch | 2026-09-18 merge | manifest sha | 2026-09-21 merge |
|---|---|---|---|---|
| #17563 | `up-repeat-guard` | `1e384e33` | `f87ae80d` | `14afe025` |
| #17564 | `up-truncated-tool-calls` | `a765e728` | `70213554` | `9976ce1c` |
| #17565 | `up-gemma4-object-close` | `5dc1d79a` | `64f89f3b` | `2184656c` |
| #17566 | `up-think-budget` | `2fd06701`, `aa991d47`, `f830f71d` | `6ae1ee7c` | `ae5a9afb` |
| #17567 | `up-mlx-libdl` | `43be0f1d` | `88108349` | `f2e943e9` |
| #17626 | `up-gemma4-stray-channel-name` | `c7d7a3fb` | `acf95e0c` | `f843a483` |
| #17914 | `qwen3coder-tolerate-malformed-tool-calls` | `27e10549` | `d272c4f1` | `7d5e86c2` |
| #18212 | `up-reasoning-budget-line-boundary` | `a6dc8df3` | `3d15e71c` | `04cf5711` |
| #18281 | `up-native-thinking-replay` | `bb54003d` | `af4825f7` | `4e2ed242` |
| #18288 | `up-gemma4-stray-closer` | `5a80b98b` | `d54fe13e` | `f9f9fea3` |
| #18289 | `up-jinja-runner-reuse` | `ddb0fec9` | `43a969b9` | `2cb18c10` |
| #18307 | `gemma4-toolcall-in-thinking` | `0c0db3d9` | `16a78556` | `48f88b14` |
| #16820 | `pull/16820/head` — **not ours** | `a4a6dd7b` | — | — |

Two branches carry no `up-` prefix on purpose: renaming a branch that heads an
open cross-fork PR closes the PR. See `docs/protocols/FORK-SYNC.md` R1.

## Fork-only — carried, never reported upstream

Manifest `bc1448ec`, `integration.sha d57818e3`, still `base: v0.34.2`. These
two arrived as `status: "fork-only"` with `upstream_pr: null` — a value no
earlier row had, flagged by the fork rather than slipped in. Keyed on the
branch slug, because there is no PR number to key on.

| branch | manifest sha | our merge | what it does |
|---|---|---|---|
| `up-gemma4-unparsed-tool-call-content` | `cbcc5ed2` | `1b9e0ab6` | a tool call the parser cannot read comes back as content, re-wrapped in its own tags, instead of vanishing |
| `up-toolcall-tags` | `61c78a05` | `ef7a0ad6` | `ToolCallTagger` / `ToolCallStartTagForParser`, plus the gemma4 and qwen3.5 methods |

`up-gemma4-unparsed-tool-call-content` deliberately does **not** repair the
call. The run that found it had a `new_text` argument degenerated to
`text=text=text=` before the key was swallowed; repairing that payload writes
the fragment into the user's file. A call the parser cannot read is a call that
must not run — but it must not vanish either, which is what used to happen:
`calls=0`, `content=""`, `thinking=""`, and only a server-log warning.

`up-toolcall-tags` is the branch this repo asked for. `ToolCallTags()` had been
on `think-budget` since August with no `up-*` home — an R2 violation found by
grepping for it after the merge that was supposed to bring it. The branch is an
extraction, not a new commit.

**Its conflict has a trap, and it is worth keeping written down.** It collides
with `up-think-budget`, which adds `ThinkingTagger` at the same three points in
the same three files (`parsers.go`, `gemma4.go`, `qwen35.go`). Same keep-both
shape as the others — except the conflict boundary cuts **inside a function**,
so the closing brace on the line after `>>>>>>>` belongs to *both* halves.
Concatenating the two sides the obvious way orphans it and yields Go that does
not parse; the fork's own replay harness did exactly that and reported four
syntax errors. Close the first block with its own brace. `ThinkingTagger` first.

**`ToolCallStartTagForParser` has no caller here yet**, and that is expected:
the half that *reads* the tag — response-scope thinking budget — is the residue
still on `think-budget`. See `docs/protocols/FORK-SYNC.md`, "Dependent patches".

**Six of the twelve conflicted, and none of it was an ordering mistake.** Four
were the shape the manifest's `known_conflicts` predicts — two additive test
blocks at the same point, resolved by keeping both. One
(`up-gemma4-stray-channel-name` on `model/parsers/gemma4.go`) was git anchoring
a one-line addition on the wrong `p.buffer.Reset()` block, since #18307 had
inserted a new one above it; the patch's own line was already in the right
branch, so HEAD was kept and all four pieces of the patch were then checked
present by name. `gemma4-toolcall-in-thinking` conflicted as a pure
duplication — the resolution left the file byte-identical to what it already
was, confirmed with `git diff`.

**One conflict was a real choice, and the fork won it.**
`up-jinja-runner-reuse` compares the config a runner was *launched* with
(`runner.llamaConfig`) rather than one recomputed from its model, and uses
`reflect.DeepEqual` because 0.34 added `DraftModelShardPaths []string` to
`LlamaServerConfig`. Theirs was taken whole, per FORK-SYNC.md R4. It then
failed four of this repo's own fixtures — `TestSchedNeedsReload` and the three
`TestSchedNeedsReloadIgnoresAutomatic*` — which build a `runnerRef` by hand and
left the new field zero, so an empty config compared against a real one and
every request reloaded. Same defect the fork fixed in two upstream fixtures;
fixed here for ours, since these tests are this repo's, not the patch's.

Gates on the whole set: `gofmt -l .` silent, `go build ./...`, `go vet ./...`,
`go test ./...` (59 packages ok, 0 failures — the four "not ours" failures of
2026-09-18 did not reproduce), and `golangci-lint run` (0 issues).

**`ToolCallTags()` did not arrive, and it is not in the manifest.** It exists on
`think-budget` and on `thinkbudget-0.34.2` — `model/parsers/parsers.go`,
`gemma4.go`, `qwen35.go` and both test files — and on **no** `up-*` branch at
any manifest sha. That makes it a patch authored on the integration branch,
which R2 forbids and R4 says we must not hand-copy. Raised with the fork; it
needs a branch and a manifest entry before it can be carried here.

Verified after the replay: `go build ./...` and `go vet ./...` clean, and the
full `go test ./...` passes except four failures that are **not ours** and fail
identically on clean v0.34.2 — `cmd/launch` (1) and `cmd/internal/fileutil` (3,
which assert permission denials and cannot fail when the suite runs as root).

**2026-09-19 — #17566 carries a second merge.** `aa991d47` brings `f3f8c342`
from `up-think-budget`: thinking switched off no longer inherits the model's
`think_budget`. A patch updated at the source gets another `--no-ff` merge
rather than a rewritten one, so rule 1 still holds — retiring #17566 is the
revert of an identifiable set of merges, now two. Verified on the merge:
`gofmt`, `go build .`, `go vet ./...`, `go test ./...` (58 ok, 0 fail, exit 0),
`go test -race` on `server api openai anthropic llm`, and `golangci-lint run`
(0 issues). The four "not ours" failures noted above did not reproduce in this
run.

**2026-09-19 — #17566 carries a third merge, and it closed the gap above.**
`f830f71d` brings `821f4705` and `fc275769`. The first is the missing
`TestShowThinkBudget` case, written on the PR branch as rule 2 requires rather
than here — which is why there was nothing to collide with. The second is a
*separate* defect found behind it: the `/api/show` response cache is keyed on
`{Model, Verbose}` plus the manifest digest and knows nothing about `Think`,
while the response carries `think_budget` and `think_budget_tokens` — answers
*about* the think value the caller intends to send. Ask `think: true` then
`think: false` and the second is served an armed budget for a request that
switched thinking off. Keyed now on the marshalled think value, because
`String()` renders `false`, `nil` and an integer budget all as `""`.

**One adaptation was needed, and the upstream PR is right as it stands.**
`server/model_show_cache_test.go` arrived importing `fs/ggml` for `ggml.KV`.
That package does not exist on v0.34.2 — this tree has `fs/gguf`, and the test
helper is `gguftest "internal/testutil/gguf"`, which is what the rest of
`server`'s tests use. Substituted `gguftest.KV`; no behaviour change, and
nothing to send upstream, because the branch is correct against the newer base
it targets. Expect the same one-line fixup on every future merge of this file
until we rebase past the rename.

Both fixes were verified by removal, not just by a green run: dropping the
cache-key field fails `TestModelShowCacheKeysOnTheThinkValue`, and dropping the
`thinkBudgetForShow` guard fails
`TestShowThinkBudget/says_nothing_when_the_caller_has_switched_thinking_off`.
Gates on the merge: `gofmt`, `go build .`, `go vet ./...`, `go test ./...`
(58 ok, 0 fail, exit 0), `-race` on `server api openai anthropic`, and
`golangci-lint run` (0 issues).

## Fork-only, second batch — manifest 18 patches (2026-09-22)

| branch | manifest sha | our merge | what |
|---|---|---|---|
| `up-codex-request-count-mtime` | `bf047765` | `585067e6` | the session's own rollout file is skipped on a coarse-mtime filesystem |
| `up-fileutil-root-permission-tests` | `e153344b` | `f721ddaf` | three permission tests skip under uid 0 |
| `up-gofmt-vision-test-data` | `09e76dda` | `42d6028c` | the one file `gofmt -l` names on v0.34.2 |
| `up-response-scope-think-budget` | `fdf0d23c` | `6cbbe858` | **the first stacked patch** |

`up-response-scope-think-budget` declares
`depends_on: [up-think-budget, up-reasoning-budget-line-boundary, up-toolcall-tags]`
and a base of `8f64c67d`, the merge of those three onto `v0.34.2` — not the tag.
The widened invariant was checked before merging rather than assumed: all three
are ancestors of the branch, all three were already on `dev`, and all three are
earlier in `patches[]`. Note that merging a stacked patch also imports its base
merges (`05e1dfcf`, `9b3b4766`, `8f64c67d`), which is harmless here because they
merge shas this tree already carried. It gives `ToolCallStartTagForParser` the
caller it was waiting for (`server/routes.go:2592`).

**Two of the four were fixes xollama had already made, locally, on upstream
files.** Both conflicted for that reason, and in both the fork's version was
taken so the two trees converge on one identifier:

- `codexAppRequestMTimeSkew` (ours, `060144ab`, 2026-09-18) vs
  `codexAppSessionMTimeSlack` (theirs). Semantically identical —
  `mtime < start-2s` either way — and measured independently on this host
  before their branch existed.
- `requirePermissionEnforcement` (ours) vs `requirePermissionsApply` (theirs).
  Theirs is the better-scoped version: it guards only the three tests that
  assert a refusal, leaving `_UnchangedContentIsNoOp` and `_NoOrphanTempFiles`
  measuring something real under root.

`060144ab` was authored here, on a file that exists at `v0.34.2`, and never sent
to the fork — it is in no branch of `mann1x/ollama`. That is the same failure as
`ToolCallTags()`, pointing the other way, and it predates the protocol that
forbids it (`FORK-SYNC.md` R4, agreed 2026-09-21). Worth stating plainly rather
than quietly resolving, because R4 exists for exactly this and it was us.

Gates on all four: `gofmt -l .` silent, `go build ./...`, `go vet ./...`,
`go test ./...` 59 ok / 0 fail, `go test -race` on `llm server model cmd` all
ok, `golangci-lint run` 0 issues.

## The 004 drift closure — merged, and what the measurement says (2026-09-22)

| branch | fork sha | our merge | what it adds |
|---|---|---|---|
| `up-response-scope-think-budget` (C++ half) | `3c5d29b4` | `84bcccc5` | `b4c58eee`: a spent response stays quiet and bars reopening |

`llama/compat/004-reasoning-budget-line-boundary.patch` goes 825 → **982 lines**.
Union verified by identifier rather than by line count: `reasoning-budget-scope`
2, `THINK_BUDGET_SCOPE` 1 (#18212's half, intact), plus `reset_seqs` 11,
`forced_end_pos` 3, `common_reasoning_budget_end_offset` 2,
`spent_response_stays_quiet` 2, `spent_response_bars_reopening` 2.

**Applied on a clean fetch**, which is the only way to check a `.patch` here —
the applier skips anything `git apply --reverse --check` accepts, so on an
already-patched tree a wrong patch is silently skipped rather than rejected, and
no Go gate reads these files at all. Fresh `FetchContent` of revision `391fac164`
(b10969) logged `llama/compat: applied 001-…, models/003-…, 004-…, 005-…`.

### The repro the fork asked us to run — it does not reproduce here

Their request was explicit: *"If the 32-copies repro does not reproduce on your
side before the change, tell me — it would mean my reading of what `b4c58eee`
fixes is wrong."* It does not.

Two arms, same Go binary, differing only in `llama-server`,
`libllama-common.so*` and `libllama-server-impl.so`; **before** = the 825-line
patch (`forced_end_pos` absent from the fetched source, confirmed by grep),
**after** = the 982-line one. `XOLLAMA_ENGINE=llamacpp` pinned, because the
opencoti engine has its own sampler and can never exercise 004.

| arm | model | budgets | runs | wrap-up copies | reopens |
|---|---|---|---|---|---|
| before | `gemma4:e2b` | 4, 8, 16, 32, 64, 128, 256 | 7 | **1 each** | 0 |
| before | `deepseek-r1:14b` | 32, 128 | 2 | **1 each** | 0 |
| before | `deepseek-r1:14b`, prompts that ask for a second `<think>` | 16, 64 × 2 prompts | 4 | **1 each** | 0 |
| after | `deepseek-r1:14b`, same four probes | 16, 64 × 2 prompts | 4 | **1 each** | 0 |

Thirteen before-arm runs, one wrap-up copy in every one, never in `content`.
The run most likely to show the bug — `reconsider-loop` at budget 16, which ran
to the 4096-token cap (`done_reason: "length"`, 15,328 characters of content) —
still carried exactly one. The engine log tells the same story: one
`activated` / `budget exhausted` / `forced sequence complete, done` per request,
never a second activation.

**What this does and does not say.** It does not contradict `b4c58eee`: the path
it guards is what happens when a model opens a **second** thinking block after
the response budget is spent, and no model here reopened, not even when asked to
in the prompt. So the repro never reached the defect, and a repro that cannot
reach the defect proves nothing either way. The after arm matching the before
arm token-for-token on all four shared probes is the useful result: on this
host, with these models, the extra 157 lines are **behaviour-neutral where the
bug does not fire**.

The merge stands on the union check, the clean-fetch apply, and the patch's own
`test-reasoning-budget` cases (`spent_response_stays_quiet`,
`spent_response_bars_reopening`) — not on this repro.

### Second round, on the fork's own tag — still one copy

The fork answered with a structural reason the first round could not have
reached the guard, and it is correct here: `ToolCallStartTagForParser`
(`model/parsers/parsers.go:60`) returns `""` unless the parser implements
`ToolCallTagger`, and in `model/parsers/` exactly two do — `Gemma4Parser`
(`gemma4.go:72`) and `Qwen35Parser` (`qwen35.go:67`). `deepseek-r1` has no entry
in `ParserForName` at all, so six of those thirteen runs got
`ThinkBudgetResetTag: ""` and the tool-call reset could not fire in principle.
`gemma4:e2b` has a parser but emitted no tool call, so nothing reset there
either.

Re-run on their recorded tag, which is on this host:
`qwen3.8-mtp_tb:27b-q4km` — `renderer qwen3.8`, **`parser qwen3.5`**, 27.3B,
whose own params carry `think_budget medium` and a 240-character budget message.
`think_budget` 96 and 64 against `num_predict` 768 and 1024 (the ratio is the
lever), `num_ctx` 8192, with and without a tool the prompt forces the model to
call. `offloaded 66/66 layers to GPU` in both arms.

| | runs | tool calls | budget activations | copies | in `content` | back to back |
|---|---|---|---|---|---|---|
| before | 6 | 0, 0, 0, 20, 21, 29 | 6 of 6 | **1 each** | 0 | 0 |
| after | 6 | 0, 0, 0, 20, 21, 29 | 6 of 6 | **1 each** | 0 | 0 |

Identical arm to arm on every field — copies, tool calls, `thinking_chars`,
`content_chars`, `eval_count`, `done_reason`. Route 2 was genuinely exercised
this time: the tools runs emit 20–29 calls, so the reset tag `<tool_call>` was
in the stream. The model still never opened a **second** thinking block — it
goes from the wrap-up message straight into the tool calls and never reopens —
so the guard's precondition does not occur, and the defect is still out of
reach rather than absent.

**Nineteen before-arm runs now, across three models and both routes, all one
copy.**

### The gate that does hold: the patch's own tests, on our own fetch

The repro is not the gate, and this is. `test-reasoning-budget` built from
**our** clean b10969 fetch (`391fac1646`) carrying the merged 982-line patch,
CPU-only:

```
Test 'spent response scope closes quietly' passed
Test 'spent response scope bars reopening' passed
Test 'response scope budget' passed
Testing reasoning budget sampler... OK (14 tests passed)
```

Built standalone with the compat hooks (`001`) temporarily reverted, because
they pull `llama/compat/*.cpp` into `libllama` and `004` lives entirely in
`common/` and `tests/` — the two files were restored afterwards. `llama/server`
forces `LLAMA_BUILD_TESTS OFF` (`llama/server/CMakeLists.txt:174`), so the
ollama sub-build will never run these; a separate configure is the only way.

### Two hypotheses retired, and the one that is left

Both were mine and both are wrong, corrected by the fork with evidence:

- **Not harness-shaped.** Their observation was a single `/api/chat`, no tools,
  no tool round trip — the model reopened on its own.
- **Not a dead path.** Reverting the two behaviours fails
  `tests/test-reasoning-budget.cpp:367` on b10969, so the sampler still
  re-enters `FORCING` on a second start tag. A base change can only alter
  whether a model *emits* that tag.

What is left is **b10434 → b10969**, and one correction that matters for reading
any of this: their two observations are opposite sides of an intermediate fix,
not one defect seen twice. **08-17** is the 32-copy loop, pre-`b4c58eee`.
**08-18** is the residual after it — loop gone, one copy in `thinking`, one
*further* copy leaking into `response`, cutting the answer mid-word with a
trailing `</think>`. Our before arm carries neither identifier, so it is the
08-17 configuration, and the loop did not happen on b10969 with their tag.

**Lane note.** These builds ran in a detached worktree, not in this checkout:
`build/lib/ollama` is a symlink to `/usr/local/lib/ollama` and a local
`llama-server` build installs straight into the live service's runtime. That
happened once, on 2026-09-22, before the lane changed. Rule 7 of the
`xollama-build-test` skill now carries it.

## Tier 1 — the reason this fork exists

| PR | Title | Branch | Opened | Age |
|---|---|---|---|---|
| [#17566](https://github.com/ollama/ollama/pull/17566) | `api`: bound thinking with a token budget, per request or per model | `up-think-budget` | 2026-08-04 | 45d |
| [#16820](https://github.com/ollama/ollama/pull/16820) | `server`: add `/api/tokenize` and `/api/detokenize` | `pull/16820/head` — **not ours** | 2026-06-19 | 91d |

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

## Tokenizer endpoints

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

**Status: #16820 is carried** as of 2026-09-18 (`a4a6dd7b`), and is the only
carried patch that is **not one of ours**. Rule 2 — "fixes go to the PR branch
first" — cannot apply to it: we cannot push to `hustxiayang:tokenization`. Fixes
therefore land here, and if this one grows past adaptation it should become our
own PR that credits the original. Two adaptations were needed at carry time: the
PR's base is 382 commits back and `scheduleRunner` had since changed from taking
a model name to taking a `*Model`, and the PR ships no tests, so
`server/routes_tokenize_test.go` was written for it.

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


## What the replay found

Five of the twelve needed more than a merge. Recording it here because each one
is also feedback for the upstream PR, which still has to land on upstream's
`main` eventually.

**#17566 (think budget) — a clean merge that did not compile.** Git resolved all
12 overlapping files by itself, and one resolution was wrong: upstream migrated
`server/routes_test.go` from `fs/ggml` to `fs/gguf` + `internal/testutil/gguf`,
so the PR's new test still called `createBinFile` with `ggml.KV`. `go build
./...` passed and said nothing — the package that failed was a test package.
Only `go vet` caught it. This is the single best argument for the protocol's
"do not skip a step because the previous one passed".

**#17563 (repeat guard) — conflict, resolved toward the PR.** Upstream's crude
`tokenRepeat > 100` abort is exactly what the PR replaces, and the variable's
declaration had already been removed in a hunk that merged cleanly, so keeping
upstream's side would not have compiled.

**#18289 (runner flags) — does not compile or pass against v0.34.2 as written.**
Two separate upstream changes broke it: `llm.LlamaServerConfig` gained
`DraftModelShardPaths []string`, so the PR's `!=` comparison is no longer legal
on the struct; and storing the launch config in a new `runnerRef` field meant
any `runnerRef` built outside `getRunner` had a zero value and was always judged
stale, which broke upstream's passing
`TestSchedGetRunnerReusesSameDigestWhenModelPathEmpty`. Resolved by deriving
both sides from their models rather than storing one — equivalent by
construction, since `runner.model` is fixed for the runner's life and is what
the launch config was computed from. **The upstream PR should be updated the
same way.**

**#17567 (libdl) — the file moved.** Upstream relocated `x/mlxrunner/mlx/` to
`mlx/`, producing a modify/delete conflict. Still needed: `mlx/mlx.go` has no
`-ldl` and `mlx/dynamic.c` is still the `dlopen` caller. Applied by hand at the
new path. **The upstream PR needs rebasing onto the new location.**

**#18288 and #17565 — both append tests to `model/parsers/gemma4_test.go`**, as
do #18281 and #17566 to `llm/llama_server_test.go`. In both cases the two blocks
shared the file's trailing brace, so a naive "keep both" leaves the first
function unclosed. Kept both, each with its own closing brace.

### One conflict that belongs to no single PR

`TestChat/TestGenerateParseErrorMidStreamDoesNotWedge` feed a qwen3.5 tool call
that closes `<parameter>` with `</function>` and assert a 500. #17914 makes that
exact shape recoverable, so the request now succeeds and the assertion fails.
The tests are about what `routes.go` does when a parser errors mid-stream, not
about which inputs a parser rejects, so they now use `qwen3-vl-thinking` with a
tool call whose JSON is cut off mid-value.

This is an **integration** conflict: it appears only when the fixes are combined,
which is precisely what this fork is and what no individual upstream PR can see.
Expect more of these as patches accumulate, and keep them in their own commits
rather than folding them into a carried merge — they are ours, not the PR's.

## Fork-originated fixes, not yet sent upstream

These are defects we found in upstream's own tree while working here. They are
not carried PRs from `mann1x/ollama` — they originate in this fork and should be
offered upstream, at which point they retire from this list like any other.

The reason they are listed rather than merely fixed: an upstream sync will
reintroduce the unfixed form if upstream has not taken them, so a reviewer needs
to know these diffs are deliberate and whose they are.

| Fix | File | Why it is upstream's | Status |
|---|---|---|---|
| Skip permission tests when running as root | `cmd/internal/fileutil/files_test.go` | uid 0 bypasses the mode bits three tests assert on, so `chmod 0o444` does not block the write and `expected error, got nil` fires. Fails identically at tag `v0.34.2`. The file already guards the same class of problem for Windows; root was simply missed. | fixed here, **to send** |
| `gofmt` | `integration/vision_test_data_test.go` | Missing blank line between a base64 const and the next doc comment. Unformatted at `v0.34.2`; CI's gofmt gate flags it on any change to the file. | fixed here, **to send** |
| Session-file mtime pre-filter loses the first prompts | `cmd/launch/codex_app_profile.go` | `start` comes from `time.Now()` (fine-grained clock); file mtimes come from the kernel's coarse clock, which advances once per timer tick. A session file written just after `start` is recorded carries an *earlier* mtime and is skipped entirely. Measured: start `14:42:19.515114114Z`, mtime of a file created after it `14:42:19.514497780Z`. | fixed here, **to send** |

### One of them is upstream-of-upstream

`llama/compat/005-gemma4-assistant-unchecked-tensor-shape.patch` belongs to
**ggml-org/llama.cpp**, not ollama/ollama, so it retires on a `LLAMA_CPP_VERSION`
bump rather than on an ollama merge. It lives in `llama/compat/` with the other
carried llama.cpp patches and is registered in that directory's README.

| Fix | File | Why it is upstream's | Status |
|---|---|---|---|
| An empty expected `ne` means "all dims must be 1", not "unchecked" | `src/llama-model-loader.cpp`, `src/llama-impl.cpp`, `src/models/gemma4-assistant.cpp` | `gemma4-assistant.cpp` passes `{}` for `masked_embd_centroids` / `masked_embd_ordering` to claim them without asserting a shape. `check_tensor_dims` instead requires `cur->ne[i] == 1` for every `i >= ne.size()`, so the real `[256 2048]` and `[262144]` tensors fail. The error path then calls `llama_format_tensor_shape(ne)`, whose first statement is `ne.at(0)` on that same empty vector, so the diagnostic is replaced by `vector::_M_range_check`. Both call sites are upstream's, and `{}` appears nowhere else in the tree. | fixed here, **to send** |

Loading a Gemma 4 **E2B/E4B** assistant drafter is impossible without this — it
is the only thing standing between those two heads and the llama.cpp engine.
12B, 26B-A4B and 31B carry no `masked_embd_*` and were never affected, which is
why this looked model-specific for so long.

The patch deliberately stops at *loading*. llama.cpp registers the architecture
but does not implement the centroid / ordered-embedding head:
`load_arch_hparams` never reads `n_centroids`, `centroid_top_k` or
`use_ordered_embeddings`, all three of which the GGUF declares. Speculative
decoding verifies every drafted token against the target, so **output stays
correct**; what degrades is the acceptance rate, i.e. speed. Implementing the
head is separate work.

A third hunk was written and then **removed after testing**, which is worth
recording. It logged a warning when the centroids were present, on the reasoning
that a head which loads and drafts badly is harder to diagnose than one that
fails. The E4B run showed it never fired *and* was redundant: llama.cpp already
prints, from `llama-model-loader.cpp:1196`,

```
model has unused tensor masked_embd_centroids.weight (size = 557056 bytes) -- ignoring
model has unused tensor masked_embd_ordering (size = 1048576 bytes) -- ignoring
```

which names both tensors and their sizes — strictly better than the warning
being added. It never fired because ollama's own compat hook (patch 001)
`should_skip_tensor` hides MTP-class tensors, so `create_tensor` returns
`nullptr` for them. The concern was real; upstream had already answered it.

The root guard follows the file's existing idiom rather than inventing one:

```go
func requirePermissionEnforcement(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission tests unreliable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced, so the error under test cannot occur")
	}
}
```

Verified in **both** directions, which is the part that matters for a skip: as
root the three tests skip and the package passes; compiled with `go test -c` and
run as `nobody`, all three genuinely execute and pass. A guard that turns a
suite green by making a test run nowhere is worse than the failure it hides.


The `cmd/launch` fix is a two-second tolerance on the mtime comparison:

```go
if err != nil || info.ModTime().Before(start.Add(-codexAppRequestMTimeSkew)) {
```

It cannot overcount. The mtime test only decides which files are worth opening;
whether a line counts is decided per line by `codexAppLineIsUserRequest`, which
enforces the same `start` against the event's own timestamp. Proven in both
directions: reverting the tolerance reproduces the failure, restoring it passes.
