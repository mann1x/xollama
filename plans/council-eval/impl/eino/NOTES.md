# eino v0.9.21: council bake-off notes

Files: `eino.go` (candidate, closure streaming; 301 lines including the native hooks),
`native.go` (native-stream experiment, 88 lines), `eino_test.go` (the suite, in the required
shape), `native_test.go` (the same suite on the native variant), `compile_test.go`
(compile cost), `cmd/main.go`.

## 1. How the flow maps onto eino

One `compose.Graph[*job, council.Result]` in Pregel mode
(`compose.WithNodeTriggerMode(compose.AnyPredecessor)`, the default). Pregel is the only mode
that allows a cycle. `AllPredecessor`, the DAG mode that `Workflow` uses, rejects cycles.

| Council step | eino construct |
|---|---|
| per-request context (cfg, model, conv, emit, draws, plan, findings, critiques, round) | `compose.WithGenLocalState(func(ctx) *state)`. The job enters the state in `decide`'s `compose.WithStatePreHandler`, and nodes read or write it with `compose.ProcessState[*state]` (mutex-guarded) |
| decide → direct / council | `InvokableLambda` node `decide`, then `g.AddBranch("decide", compose.NewGraphBranch(...))` to `direct` or `plan` |
| direct | `InvokableLambda` → `END` |
| plan → researchers ∥ | `plan` node + `compose.NewGraphMultiBranch` that selects `researcher_0..n-1` out of `slots` pre-declared nodes |
| researcher fan-in | each researcher returns `map[int]string{i: text}` and has an edge to `findings`. Pregel fires `findings` in the next superstep with all values merged by eino's built-in map merge, a reflection-based reducer in `internal/merge.go:62` that errors on duplicate keys. `findings` orders the map by index into `state.findings` |
| critics ∥ + fan-in | same shape: `findings` multi-branch → `critic_i` → `verdict` (map merge) |
| bounded loop | `verdict` calls `council.NeedsRevision`. Its `NewGraphMultiBranch` either returns to `researcher_0..n-1`, which is a real graph cycle, or goes to `synthesize` → `END`. The value on every member edge is the round number |
| step bound | `compose.WithRuntimeMaxSteps(3 + 4*MaxRounds + 2)` per call |

Every model call goes through the `council.*` step functions, and nothing else talks to the model.

## 2. Streaming

**Candidate (`Runner{}`): closures.** Each member gets the `council.Serial(emit)` from the state and
calls it from its own node goroutine. The suite passes, including the slow-consumer test, and
there is no ordering problem: synthesizer content is never preceded by late thinking (0 late
events in 20 slow-consumer runs).

**Native (`Runner{Native: true}`): it works, but it needs glue.**
- Researcher and critic nodes are `compose.StreamableLambda` returning `*schema.StreamReader[map[int]string]`. Each token is one `{i: token}` chunk. A goroutine behind a `schema.Pipe` runs `council.Research`/`Critique`.
- Under `Invoke`, eino converts the stream to a value with `invokeByStream`, concatenating map chunks per key by reflection (`internal/concat.go` `concatMaps`). The same map-merge fan-in then works unchanged.
- The tokens reach `emit` through a per-run `callbacks.Handler` passed with `compose.WithCallbacks`. Its `OnEndWithStreamOutputFn` receives eino's copy of each node's output stream (`internal/callbacks/inject.go:180`), identifies the member by `RunInfo.Name` (you must set `compose.WithNodeName`, because the name defaults to empty), and drains the copy in a goroutine.

Costs and footguns I found:
- **The tokens are the node's output, so they must flow even when deliberation is hidden.** I call the step with `ShowDeliberation=true` and the forwarder drops the events instead.
- **Two extra goroutines per member** (producer and callback drainer). Peak goroutines at width 8 went from 11 to 27.
- **The producer and drainer goroutines outlive the graph**, because a node "finishes" when it returns its stream. `Run` has to hold a `WaitGroup` and wait on it after `Invoke`, or goleak and in-flight checks fail.
- **Ordering bug, which the suite does not catch.** The drainers run at the consumer's pace, so under a slow consumer the synthesizer's content went out before the members' thinking. I measured 3940 thinking events after the first content over 20 runs. I fixed it with a hand-made drain barrier (`job.drain.Wait()`) before `Synthesize`, which brought it to 0.
- **More cost:** allocations rose from 686 to 1017 per council run, and ns/op rose about 35%.

Direct, plan and synthesize stay on closures in both variants.

## 3. Cancellation and failure

- **Cancelled ctx:** the library is adequate. Every node gets the Invoke ctx, the stub returns `ctx.Err()`, and the error is wrapped in `internalError`, which implements `Unwrap`, so `errors.Is(err, context.Canceled)` holds. The run loop also checks `ctx.Done()` between supersteps (`compose/graph_run.go:254`).
- **Failing member: eino does not cancel its siblings.** In Pregel / needAll mode, `taskManager.wait` calls `waitAll()` and collects the whole superstep before it looks at any error (`compose/graph_manager.go:361-365`). With my cancel removed, the failure test took **1.03 s** (the 250 ms bound failed) because the sibling researcher ran to completion.
- **What I added:** `context.WithCancelCause` per Run. Each member calls `cancel(err)` on failure, and `Run` returns `context.Cause` in preference to the graph error, so the injected error wins over the siblings' `context.Canceled`. That order is otherwise completion order.
- **No goroutine leak:** in closure mode nothing leaks (`waitAll` joins every task). The native variant needed the extra `WaitGroup` described above.
- **Eager mode is not a fix.** With `AllPredecessor` the graph would return on the first error but leave siblings running, and it cannot express the cycle anyway.

## 4. Dynamic width; compile once or per request

- **The topology is static,** so a runtime width means pre-declaring `slots` researcher and critic nodes and letting a `NewGraphMultiBranch` pick the first `cfg.Researchers` / `cfg.Critics`. Unselected slots simply don't fire. The graph is compiled with 8 slots. A wider request compiles a larger graph once and caches it (`sync.Map` + `sync.Once`, keyed by slot count).
- **Compiled once per Runner, but not free per request.** Compiling costs 239 µs, 75 KB and 1438 allocs (`BenchmarkCompile`). Even so, every `Invoke` rebuilds a channel per node in `initChannelManager` (`compose/graph_run.go`, about line 1080). That is 53% of the allocations on the direct path, and it scales with the slot count even when only one node runs.
- **The default step limit scales with node count, not iterations.** It is `len(nodes)+10` (`compose/graph.go:884`), so a loop hits `ErrExceedMaxSteps` depending on how many slots you declared. I set `WithRuntimeMaxSteps` per call.

## 5. Results

`gofmt -l impl/eino`: empty. `go vet ./impl/eino/...`: clean.

`go test -race -count=3 ./impl/eino/`, run 7 times across the session: every run passes. All 11 subtests pass for both
`TestCouncil` (candidate) and `TestNativeCouncil`: direct, council, findings in researcher order,
hide deliberation, seeds and jitter, parallel width 3, parallel width 8, bounded loop, cancel,
member failure cancels its siblings, slow consumer loses nothing. I saw no flakiness.

Benchmark (`-benchtime 2s -count 1`). The machine was shared with two other agents
benchmarking at the same time, so the µs-scale rows are noisy (±30%):
```
BenchmarkCompile-12                                9147     239166 ns/op   74944 B/op  1438 allocs/op
BenchmarkCouncil/council/state=1KB-12             12758     178146 ns/op   63387 B/op   686 allocs/op
BenchmarkCouncil/council/state=64KB-12            17109     127933 ns/op   63131 B/op   686 allocs/op
BenchmarkCouncil/council/state=1024KB-12          19122     154381 ns/op   62736 B/op   686 allocs/op
BenchmarkCouncil/direct-12                        35234      63213 ns/op   31278 B/op   301 allocs/op
BenchmarkCouncil/fanout/width=3-12                   30   76935071 ns/op   6.000 peak-goroutines   1.017 x-ideal
BenchmarkCouncil/fanout/width=8-12                   30   78681258 ns/op   11.00 peak-goroutines   1.042 x-ideal
BenchmarkCouncil/ttft-12                             22  101000918 ns/op   53807 council-first-content-µs   4428 council-first-thinking-µs   4432 direct-first-content-µs
BenchmarkNativeCouncil/council/state=1KB-12       10555     244216 ns/op   80506 B/op  1017 allocs/op
BenchmarkNativeCouncil/council/state=64KB-12      10000     268733 ns/op   80700 B/op  1017 allocs/op
BenchmarkNativeCouncil/council/state=1024KB-12    10000     227833 ns/op   80691 B/op  1017 allocs/op
BenchmarkNativeCouncil/direct-12                  56476      50887 ns/op   33474 B/op   339 allocs/op
BenchmarkNativeCouncil/fanout/width=3-12             30   78159516 ns/op   12.00 peak-goroutines   1.033 x-ideal
BenchmarkNativeCouncil/fanout/width=8-12             30   80053949 ns/op   27.00 peak-goroutines   1.060 x-ideal
BenchmarkNativeCouncil/ttft-12                       22  101628189 ns/op   54526 council-first-content-µs   4427 council-first-thinking-µs   4426 direct-first-content-µs
```
Baseline, same session, for comparison: council/1KB ran 33 µs, 14.5 KB, 159 allocs. Direct ran
3.0 µs, 2.3 KB, 28 allocs. Fanout x-ideal was 1.01–1.02 with 7 and 12 peak goroutines. TTFT was
the same within noise. Repeated `direct` runs: eino 56–62 µs vs baseline 2.6–3.5 µs. That is
**about 20× on the direct path and about 5× on a council run in orchestration overhead**.
Allocations are 301 vs 28 and 686 vs 159. Wall time with a real model is unaffected: x-ideal is
about 1.02–1.04.

- Binary (`-trimpath -s -w`, `cmd`): **14,680,329 bytes**.
- `go list -deps ./impl/eino/cmd`: **270** packages total, **123** non-std.
- Third-party modules pulled in by `compose` alone: sonic (+loader, golang-asm, cpuid, base64x, x/arch), gonja (+pyfmt, emperror, logrus, pkg/errors, go-humanize, yaml.v3, go-toml, filepathx, x/exp), eino-contrib/jsonschema, wk8/go-ordered-map, buger/jsonparser, json-iterator, modern-go/*, mailru/easyjson, google/uuid, bytedance/gopkg. goleak/testing come from `suite`, not eino.
- `wc -l eino.go` is 301. The closure-only candidate is about 230 of those lines; `native.go` adds 88.

## 6. Friction, surprises, footguns

- **Adding nodes and edges has to follow declaration order, and the first build error is sticky.** An edge or branch to a node not yet added fails ("branch end node 'direct' needs to be added to graph first", `compose/graph.go:490`). The failure is stored in `g.buildError` (`graph.go:163-173`), so every later call, including `Compile`, reports the same error. You declare all nodes first, then wire them.
- **Types at node boundaries are checked by reflection at build or compile time, not by the Go compiler.**
  - Every node is `func(ctx, any, ...any) (any, error)` inside `composableRunnable`.
  - Branches type-assert `input.(T)` (`compose/branch.go:60`).
  - Fan-in merges maps by reflection (`internal/merge.go:62`), and stream concat goes through `reflect.Call` (`internal/concat.go`).
  - A wrong edge type is a runtime `Compile` error.
- **Fan-in is a merge, not a join with an order.** Order is lost, hence `map[int]string` plus re-sorting. There is no merge for plain `string` or struct values unless you call `RegisterValuesMergeFunc` / `RegisterStreamChunkConcatFunc`. Those are **global, process-wide registries** (package maps, no lock), which is a hazard inside a server.
- **No sibling cancellation on failure**, as covered in §3. The superstep barrier waits for everyone (`graph_manager.go:361`).
- **The default max-steps depends on node count** (`graph.go:884`), as covered in §4.
- **Per-Invoke cost is proportional to graph size** (`initChannelManager`), so pre-declared width slots are paid on every request, including `direct`.
- **The first task of a superstep runs synchronously on the caller goroutine** (`graph_manager.go:346`). That is harmless here, but a member that ignores ctx blocks the graph loop itself.
- **Node names for callbacks default to empty** unless `WithNodeName` is set. Node keys are not passed to `RunInfo`.
- **Stream nodes "finish" when they return the reader**, so goroutines behind a stream outlive the node and the graph. Leak-proofing them is the caller's job.
- **Dependency weight:** `compose` pulls in sonic (JIT JSON with an assembler), gonja (a Jinja template engine), jsonschema and uuid, even for a graph of plain lambdas.

## 7. Verdict for xollama's server

eino can express the council faithfully: a real branch, a real cycle, a map-merge fan-in, per-run
state, and compile-once. At model speed its wall-clock cost is invisible (x-ideal about 1.02–1.04,
same TTFT). As a hot-path request library, though, it is a poor fit. It adds about 20× the
baseline's orchestration overhead on the direct path (about 60 µs and 300 allocations, mostly
rebuilding per-node channels) and about 5× on a council. It does not cancel siblings when a
member fails, so I had to add that. Width has to be faked with pre-declared slots that every
request pays for. Node boundaries are `any` plus reflection, with process-global merge and concat
registries. Its native streaming needs a callback tap, extra goroutines, a hand-made WaitGroup
and a drain barrier to stay correct. It also pulls sonic, gonja and about 25 third-party modules
into a binary that currently has none of them. The graph buys nothing over the 70-line errgroup
baseline for this fixed-shape flow. I would not put it in xollama's request path. It would only
be worth revisiting if we wanted eino's model and tool components or its checkpoint and interrupt
machinery wholesale.
