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

**Status as of 2026-09-18: all thirteen are carried on `main`, replayed onto
v0.34.2, and all thirteen are still OPEN against `ollama/ollama`.**

| PR | merge | PR | merge |
|---|---|---|---|
| #17563 | `1e384e33` | #17914 | `27e10549` |
| #17564 | `a765e728` | #18212 | `a6dc8df3` |
| #17565 | `5dc1d79a` | #18281 | `bb54003d` |
| #17566 | `2fd06701` | #18288 | `5a80b98b` |
| #17567 | `43be0f1d` | #18289 | `ddb0fec9` |
| #17626 | `c7d7a3fb` | #18307 | `0c0db3d9` |
| #16820 | `a4a6dd7b` | | |

Verified after the replay: `go build ./...` and `go vet ./...` clean, and the
full `go test ./...` passes except four failures that are **not ours** and fail
identically on clean v0.34.2 — `cmd/launch` (1) and `cmd/internal/fileutil` (3,
which assert permission denials and cannot fail when the suite runs as root).

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
