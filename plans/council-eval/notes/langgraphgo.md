# langgraphgo (github.com/smallnest/langgraphgo v0.8.5, `graph` package)

> The implementation this note describes was removed on 2026-09-26, after
> Phase 1 chose the in-house `errgroup` runner. The `impl/…` paths and
> commands below refer to commit `5271cdf4`.


Files: `langgraphgo.go` (runner, 225 lines), `langgraphgo_test.go` (suite shim),
`cmd/main.go` (size probe), `probe/probe_test.go` (library-only probes behind
every claim below; `go test -v ./impl/langgraphgo/probe/`). Library paths are
relative to `$GOMODCACHE/github.com/smallnest/langgraphgo@v0.8.5/graph/`.

## 1. Flow to library constructs

| Council step | Construct |
|---|---|
| state | `graph.NewStateGraph[state]()`, a typed struct. The request's pieces (cfg, model, conv, Serial emit, draws, cancel) ride in it as a `*run`, so the compiled graph holds no per-request closures |
| decide → direct / plan | node `decide` plus `AddConditionalEdge("decide", …)` returning `"direct"` or `"plan"` |
| direct | node `direct` → `AddEdge(…, graph.END)` |
| researchers ∥ | nodes `research/0..R-1`. Static edges `plan → research/i` and `revise → research/i`. The engine runs every node of a superstep in its own goroutine (`executeNodesParallel`, state_graph.go:490-566) |
| critics ∥ (fan-in) | edges `research/i → critic/j` for all i, j. The next superstep's node set is deduplicated (state_graph.go:655-688), so each critic runs once after all researchers finish, which gives a BSP barrier |
| bounded loop | `AddConditionalEdge(critic/j, …)` → `"revise"` if `council.NeedsRevision`, else `"synthesize"`. Every critic evaluates it over the same merged state, so they agree and the set dedups. Node `revise` bumps `Round`, moves critiques to `Prior` and allocates fresh slots, then fans out again. The bound comes from NeedsRevision/MaxRounds, not from the library (there is no recursion limit) |
| reducer | `SetStateMerger(TypedStateMerger[state])`. Members return a delta (`out{role,i,text}`) and the merger writes it into its slot by index. Order-independent by construction, because the library's order is random (see 2) |
| synthesize | node → `END`; `Rounds = Round+1` |

`StructSchema`/`FieldMerger` are the other reducer mechanism. They are
pairwise and reflection-based (schema.go:72-93, 124ff); the typed merger was
simpler and reflection-free. Nothing crosses a node boundary as `any`.

## 2. Streaming

**Closures.** Member tokens go straight from the model callback to
`council.Serial(emit)`. The library is not involved, so there are no drops,
per-member order is kept, and the slow-consumer test passes.

**Native streaming, tried in `probe/`.** It does not work for this use:

- **`StreamModeMessages` is a stub: TRUE.** It passes only
  `EventLLMStart/EventLLMEnd` (streaming.go:124-127), and no non-test code
  emits those or `EventToken` (listeners.go:40-46). The only per-node hook is
  the listener stream (start / complete / error per node, listeners.go:203-218).
  The only way to push tokens is to hand-call `ListenableNode.NotifyListeners(ctx,
  EventToken, stateCarryingTheToken, nil)` from inside the node. Probe: in
  Messages mode 0 of 5000 such tokens arrived.
- **Events dropped on backpressure: TRUE.** `emitEvent` does a non-blocking send
  and on a full channel only increments a counter; `EnableBackpressure` does
  not block (streaming.go:99-109, 153-159). Probe, Debug mode, 1000 buffer,
  20 µs consumer: 3880 of 5000 delivered and 1120 silently dropped.
- **A goroutine per listener per event: TRUE**, and it does not even buy
  non-blocking: `NotifyListeners` spawns one goroutine per listener and then
  waits for all of them with `wg.Wait()` (listeners.go:171-200). Per token that
  is a spawn plus a join on the model's goroutine.
- **Extra, worse: cross-request leakage.** `StreamingRunnable.Stream` adds its
  listener to the *shared graph's* nodes (streaming.go:203; same in
  `ListenableRunnable.Stream`, listeners.go:359). Two concurrent requests on
  one compiled runnable each receive the other's events. Probe: foreign events
  seen `[49 50]` of 50. To stream natively you must compile a graph per request.
- **Extra:** every `Stream` sleeps 10 ms before closing (streaming.go:215).
  Probe: a one-node stream takes 10.5 ms. `ListenableRunnable.Stream` does
  blocking sends of the chain start/end events (listeners.go:374, 384), so a
  consumer that stops reading leaks the goroutine. `emitEvent` checks `closed`
  under RLock and sends after unlocking (streaming.go:87-101). That is a
  check-then-act race with `close(eventChan)`, and the resulting panic would be
  swallowed by the listener's `recover` (listeners.go:187-192).

## 3. Cancellation and failure

- **No `ctx.Done()` check between supersteps: TRUE.** The superstep loop
  (state_graph.go:238-375) never looks at ctx. The only ctx check in the engine
  is the retry backoff (state_graph.go:432-437). Probe: `Invoke` on an
  already-cancelled ctx ran all 5 ctx-ignoring nodes and returned `nil`.
- **A failing node does not cancel its siblings.** `executeNodesParallel`
  simply `wg.Wait()`s (state_graph.go:564). Probe: the sibling ran its full
  200 ms and was never told.
- **The returned error is the lowest-index error of the superstep**
  (state_graph.go:313-335), and that index follows the random fan-out order.
  So once siblings are cancelled, `Invoke` can return a sibling's
  `context.Canceled` instead of the real failure. Probe: 22 of 200 runs did.
- **Added by me:** `context.WithCancelCause` per Run. A `node()` wrapper checks
  `ctx.Err()` on entry and calls `cancel(err)` when the node fails. `Run`
  returns `context.Cause(ctx)` when set, which gives the real failure or the
  caller's `Canceled`. With those, `cancel` and `member failure` pass under
  goleak and inside 250 ms. Panics in nodes are recovered by the library
  (`SafeGo`, utils.go:97-121); that part is good. The engine spawns only
  goroutines it joins, so nothing leaks.

## 4. Dynamic width; compile once

- A node is a name registered before `Compile`. A conditional edge returns one
  name (state_graph.go:659-667). A runtime list of targets exists only through
  `Command{Goto: []string}`, and it is recognised by `any(res).(*Command)`
  (state_graph.go:575). That works only when the state type is an interface,
  i.e. `StateGraph[any]` (as in the library's own command_test.go), which
  throws away the typed state. There is no LangGraph `Send` (no per-branch
  input). `AddParallelNodes`/`AddMapReduceNode` also take a fixed map of
  functions.
- **Chosen:** one graph per `(researchers, critics)` shape, built and compiled
  lazily and cached on the `Runner` (mutex + map, at most 64 shapes for width ≤ 8).
  `Compile` itself does nothing beyond checking the entry point
  (state_graph.go:142-151), and a compiled `StateRunnable` is safe for
  concurrent `Invoke` (it reads only). Measured build+compile: 1.9 µs / 23 allocs
  at 2×2, 25 µs / 176 allocs at 8×8. That is cheap enough to do per request,
  but the cache makes it free.
- Alternatives I did not take: (a) pad to max width 8 with no-op nodes, which
  costs 8+8 goroutines per round whatever the width; (c) `StateGraph[any]` +
  `Command`, which is dynamic but untyped.

## 5. Results (AMD Ryzen 5 5600G; other agents were benchmarking concurrently)

`gofmt -l` clean, `go vet` clean. `go test -race -count=3` and then
`-count=10`: **all 11 subtests PASS in every run, no flakes**
(direct, council, findings in researcher order, hide deliberation, seeds and
jitter, parallel width 3, parallel width 8, bounded loop, cancel, member failure
cancels its siblings, slow consumer loses nothing).

```
BenchmarkCouncil/council/state=1KB-12         	   40561	     69239 ns/op	   19439 B/op	     223 allocs/op
BenchmarkCouncil/council/state=64KB-12        	   27020	     87958 ns/op	   19252 B/op	     223 allocs/op
BenchmarkCouncil/council/state=1024KB-12      	   21098	    107122 ns/op	   19325 B/op	     223 allocs/op
BenchmarkCouncil/direct-12                    	  137140	     16625 ns/op	    4694 B/op	      59 allocs/op
BenchmarkCouncil/fanout/width=3-12            	      30	  76216481 ns/op	         7.000 peak-goroutines	         1.007 x-ideal
BenchmarkCouncil/fanout/width=8-12            	      30	  78506246 ns/op	        12.00 peak-goroutines	         1.037 x-ideal
BenchmarkCouncil/ttft-12                      	      22	 112688015 ns/op	     60579 council-first-content-µs	      5036 council-first-thinking-µs	      4908 direct-first-content-µs
```

Baseline, same run window: council 1KB 54.5 µs / 159 allocs; direct 2.7 µs /
28 allocs; fanout x-ideal 1.006 / 1.023 with the same peak goroutines (7 / 12);
ttft first content 53.8 ms. The overhead is about 14 µs per `Invoke` on the
direct path (one `uuid.New()` from crypto/rand per Invoke, state_graph.go:206;
a goroutine + WaitGroup per superstep even for a single node; set/map/slice
churn in `determineNextNodes`), plus about 15-50 µs on the council path.
Against real model calls that is negligible: x-ideal is within 1-4 % of the
baseline. An empty one-node `Invoke` costs 2.7 µs / 15 allocs.

Binary (`-trimpath -s -w`): **4,276,489 bytes** vs 2,244,770 for the same
program on the baseline runner (+2.0 MB). `go list -deps`: **215** packages,
**28** with a dot (baseline program: 85 total). The `graph` package imports
`tmc/langchaingo/llms` (add_messages.go:7), which brings `pkoukk/tiktoken-go`,
`dlclark/regexp2` and `net/http`; plus `google/uuid` and langgraphgo's
`store`, `store/file` and `store/memory` (checkpointing.go). LOC: `langgraphgo.go`
225.

go.mod/go.sum: `go get github.com/smallnest/langgraphgo/graph@v0.8.5` (under
the lock) was needed for the langchaingo go.sum entries. It added
langchaingo v0.1.14, tiktoken-go v0.1.6 and regexp2 v1.10.0 as indirect, and
**MVS raised `golang.org/x/exp` from 2023-07-13 to 2024-08-08**, the minimum
langchaingo requires. That change was forced; no other version moved.

## 6. Friction, footguns, bugs (library)

1. **Fan-out order is random: TRUE.** Next nodes come from ranging a map
   (state_graph.go:685-688). `AddParallelNodes`/`AddMapReduceNode` build their
   node list from a map too (parallel.go:215, 284). Probe: 4 distinct merge
   orders over 200 runs of a 4-way fan-out. An append reducer therefore
   scrambles findings; slot-by-index is mandatory.
2. **With no merger/schema, a parallel superstep keeps only the last result**
   (state_graph.go:632-634), and "last" is random. The other branches' work is
   silently lost.
3. Sibling cancellation, error choice and ctx between steps: see 3.
4. Streaming: drops, the Messages stub, cross-request listener leakage, the
   10 ms sleep, and the close race (see 2).
5. `Config.Timeout` is declared (callbacks.go:57-58) and never read.
   `TimeoutNode` returns on timeout but abandons the node goroutine
   (retry.go:138-150), which leaks if the node ignores ctx.
6. `ParallelNode.Execute` (the `AddParallelNodes` construct) also waits for
   every branch after a failure, and it wraps the error as
   "parallel execution failed" (parallel.go:145-204).
7. Conditional edges return one target. A conditional fan-out needs a join
   node (the extra `revise` superstep here) or `Command`, which needs
   `StateGraph[any]`.
8. `any`/reflection exist only where you opt in: `Command.Update/Goto any`,
   the reflection mergers in schema.go, and JSON round-trips of the state for
   callbacks (utils.go:48-80, only with a `Config`). This runner uses none of
   them.
9. The `graph` package drags in langchaingo + tiktoken + net/http although
   `graph` does no LLM work (only `add_messages.go` needs it).

## 7. Verdict for xollama's server hot path

Workable only as a thin, typed superstep scheduler, and not worth the
dependency. The core engine (typed state, conditional edges, BSP supersteps,
cycles) maps the council cleanly and adds only about 15-50 µs per request, but
every property the council depends on had to be supplied around it:

- sibling cancellation and the correct error (the library returns a random
  sibling's `Canceled`);
- ctx checks between steps;
- order-stable merging (fan-out order is a map iteration);
- dynamic width (topology is compile-time; I cached one graph per shape);
- token streaming (closures).

The library's own streaming is unusable in a server: it drops events under load
and its Messages mode is a stub. Worse, it leaks events between concurrent
requests that share a compiled graph, and it adds a fixed 10 ms per request. It
also costs about 2 MB and around 130 packages (langchaingo, tiktoken) for code
we do not call. The resulting 225 lines do what the 70-line errgroup baseline
does, with more footguns to guard against, so I would not put it on xollama's
request path.

## Addendum (2026-09-25, from the harness owner)

`go test -race ./impl/langgraphgo/probe` fails with a DATA RACE:
`StreamingRunnable.Stream` closes the channel (`graph/streaming.go:218`) while
`StreamingListener.emitEvent` is still sending on it (`graph/streaming.go:101`).
This is the close race described above, now confirmed by the race detector.
The probe passed without `-race`. It was removed with the implementation on
2026-09-26 and can be found in git history at `5271cdf4`.
