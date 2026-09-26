# council-eval — Phase 1 of the agentic council chat

A throwaway harness, and its **own Go module** (`councileval`), so no
candidate library ever touches xollama's `go.mod`. It builds the council from
[`../agentic-council-chat.md`](../agentic-council-chat.md) once per candidate
and measures each one the same way. The results and the decision are in that
plan.

| Path | What it is |
|---|---|
| `council/` | The shared core: types, prompts, per-role seeds and ±2 % jitter (`NewDraws`), and the steps (`Decide`, `Direct`, `MakePlan`, `Research`, `Critique`, `NeedsRevision`, `Synthesize`). Every runner calls only these to reach the model, so every runner does identical model work. |
| `stub/` | A model that costs a fixed time per token. It records every call plus in-flight, peak and per-role concurrency, and can inject a failure. |
| `suite/` | The one test and benchmark suite every runner passes. It covers the routes, call counts, event tagging, researcher order, hidden deliberation, seeds and jitter, parallel widths 3 and 8, the bounded loop, cancellation with goleak, sibling cancellation on failure, and a slow consumer. |
| `engine/` | `council.Model` over an opencoti `/v1/chat/completions` endpoint. Each member states its own `num_ctx`, a 429 is waited out, and the planner's calls share one session. |
| `impl/baseline/` | The council on `errgroup`, the runner Phase 1 chose. |
| `notes/` | The findings for eino, langgraphgo and trpc-agent-go, with file:line evidence in each library's source. Their implementations were removed after the decision (2026-09-26), so the repo holds no `go.mod` that would link them. They are in git history at `5271cdf4`. |
| `cmd/council-run/` | Runs the runner against a real engine: one council and one "Hello!". It records every close answer and any leaked session. |
| `probe/` | The Phase 0 Python probes (PolyKV pool tree, routing, direct-path cost). |

```sh
go test -race -count=3 ./impl/...                              # the suite
go test -run '^$' -bench . -benchtime 2s ./impl/<runner>/      # overhead, fan-out, TTFT
go run ./cmd/council-run -url http://127.0.0.1:38311 -doc <file> -out <json>
```

Run engines as the `ollama` user and write results under
`/srv/ml/xollama-phase2/as-ollama/` (`.claude/rules/solidpc-testing.md`).
