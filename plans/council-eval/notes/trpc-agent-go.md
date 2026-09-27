# trpc-agent-go v1.11.2: council bake-off notes

> The implementation this note describes was removed on 2026-09-26, after
> Phase 1 chose the in-house `errgroup` runner. The `impl/…` paths and
> commands below refer to commit `5271cdf4`.


Files: `trpcagent.go` (301 lines, 244 of them code), `trpcagent_test.go`, `cmd/main.go`.
Measured 2026-09-25 on an AMD Ryzen 5 5600G (12 threads) with go1.26.0. Library paths are
relative to `$GOMODCACHE/trpc.group/trpc-go/trpc-agent-go@v1.11.2/`.

## 1. Construct: the `graph` package, not the agent layer

I built the council as a `graph.StateGraph`. It is compiled once per `Runner` (`sync.Once`)
into one shared `*graph.Executor`, which runs Pregel/BSP supersteps.

| Council step | Graph construct |
|---|---|
| decide | node `decide` → `AddConditionalEdges(decide, fn, {"direct": direct, "council": plan})` |
| direct | node `direct` → finish point |
| plan | node `plan` → static edge to `fan_research` |
| researchers ∥ | node `fan_research` returns `[]*graph.Command{GoTo: "research", Update: {"member": i}}`, one per `cfg.Researchers`. Each `research` task writes `slot{i, text}` into `findings`, merged by a schema `Reducer` (`slotReducer`) that places each result at its index |
| join | implicit in BSP. Every task of a superstep finishes before the next one is planned, and the N writes on the `research → fan_critics` edge trigger `fan_critics` once |
| critics ∥ | the same pattern: `fan_critics` → `critic` ×N → `critiques` reducer |
| loop | node `gate` (`council.NeedsRevision`) → `AddConditionalEdges(gate, …, {"revise": fan_research, "done": synthesize})`. This is a real graph cycle, bounded by `cfg.MaxRounds` inside `NeedsRevision` and by `WithMaxSteps(1000)` |
| synthesize | node `synthesize` → finish point |
| Result | read from the executor's completion event: `StateDelta["answer" / "route" / "round"]`, then `json.Unmarshal` |

Per-request dependencies travel in the state under `council_run`, as a `*run` with
`DisableDeepCopy`, a `DeepCopier` that returns itself, and a `MarshalJSON` that returns `null`.
It holds the model, cfg, draws, the serialized emit and the cancel func. `conv` sits in the state
as an ordinary schema field. All model work goes through the `council.*` step functions; I did
not adapt anything to the library's `model.Model`.

**Why not the agent layer.**
- `parallelagent.New` fixes its sub-agents at construction (`agent/parallelagent/parallel_agent.go:48`), so a runtime width means building agents per request.
- It never cancels siblings.
- Its merge goroutine returns on an emit error without draining the sub-agent's channel (`parallel_agent.go:372-374`). That can strand the sub-agent's sender.
- `chainagent`/`cycleagent` would need an escalation event to break the loop, and route selection would need a custom agent.

The graph package expresses route, fan-out, reducers and the cycle directly.

## 2. Streaming

The default path is closures: the node passes the `council.Serial` emit into the step function.

Library-native streaming is available with `Runner{Native: true}`. Each token goes through
`graph.GetEventEmitterWithContext(ctx, state).EmitCustom("council", ev)` into the executor's
event channel, and `Run` decodes it and forwards it to `emit`. It passes the whole suite under
`-race -count=3`, including the slow consumer. What I observed:

- **Drops:** none in normal runs, because sends block (`EmitWithoutTimeout`), so a slow consumer applies backpressure.
- **Ordering:** per-member order is preserved, and members interleave. Once ctx is cancelled, events are dropped, and each drop logs a zap `WARN` line to stderr with the whole event struct (`event/event.go:487`).
- **Buffering:** the executor channel holds 256 events (`graph/executor.go:48`), so members can run up to 256 events ahead of the consumer.
- **Cost:** every token becomes an `*event.Event` with a fresh uuid (`event/event.go:334`), and its payload is `json.Marshal`ed into `StateDelta` (`graph/events.go:536-546`). The consumer then has to `json.Unmarshal` it back into a typed struct, because the payload is `any` sent as JSON.
- **Measured (1 KB council):** 250 KB/op and 2534 allocs/op native, against 229 KB and 2263 closure-based. That is about +270 allocs and +21 KB per run for roughly 40 events. Wall time is about the same, because the orchestration overhead below dominates.
- **Footgun:** `graph.GetEventEmitter(state)` uses `context.Background()` (`graph/emitter.go:275-306`), so an emitter whose consumer has gone blocks forever. You must use `GetEventEmitterWithContext`.

## 3. Cancellation and failure

**What the library does:**
- ctx cancellation reaches the nodes, because the executor passes a context derived from `Execute`'s ctx.
- It does **not** cancel siblings when a task fails. `executeStep` runs a worker pool and `wg.Wait()`s for every task (`graph/executor.go:2584-2625`); the step ctx is only cancelled by its `defer`.
- Node errors reach the caller only as a **string** in an error event (`emitTerminalGraphErrorEvent`, `graph/executor.go:1816-1861`, `err.Error()`), so `errors.Is` is lost.

**What I added:** every node is wrapped (`node()`) so that its first error is recorded on the run and triggers `context.WithCancelCause`'s cancel. `Run` returns that recorded error.

I verified this by removal. Without the cancel, "member failure cancels its siblings" fails: `took 1.026s: the sibling researcher ran on after the failure`.

**Leaks:** goleak is clean. The executor goroutine exits once `Run` drains the event channel until it closes (`graph/executor.go:292-345`). The library leaves no background goroutines.

## 4. Width, compile-once and state copying

**Dynamic width:**
- `[]*Command` fan-out enqueues one task per command (`graph/executor.go:4115-4122`, `enqueueCommands` at 4159-4223). Each task gets a full `maps.Copy` of the global state plus its overlay.
- A node that returns `[]*Command` cannot also update the global state: `Command.Update` goes only into that task's input (4186-4195). That is why the separate `fan_*` dispatcher nodes exist.
- Parallelism is capped by `WithMaxConcurrency`, which defaults to `GOMAXPROCS` (`graph/executor.go:55-61`, `workerCount` 2633-2644). On a 4-core host, width 8 would silently run 4 at a time. I set it to 64.
- `MaxSteps` is fixed per executor and defaults to 100 (`graph/executor.go:49`). Each round costs 5 supersteps, so a per-request `MaxRounds` has to fit under an executor-wide constant.

**Compile once:**
- `Executor` is documented as reusable across concurrent runs (`graph/executor.go:63-72`). I checked this with an ad-hoc test (since deleted): 16 concurrent runs of mixed width and rounds on one Runner, `-race -count=3`, all passed.
- Conditional edges and `Command.GoTo` still write to the **shared compiled graph's** channel manager on every request (`graph/executor.go:4336-4337` and `4688-4689`). The writes are idempotent and mutex-guarded, but they are writes to shared state on the hot path.
- Each request still costs an `agent.NewInvocation` (uuid), a stream hub, a per-execution channel manager and the executor goroutine.

**Deep copy (confirmed):**
- Every task's input is `State.deepCopy`'d: `buildTaskStateCopy` (`graph/executor.go:3701`) → `state.go:236` → `deepCopyAny` (`graph/utils.go:73`), which falls back to reflection (`utils.go:704`).
- Reducer updates are deep-copied twice (`state.go:302-346`).
- Strings share their bytes, so the deep copy costs O(values), not O(bytes).

**The real O(bytes) cost is JSON, not the deep copy.**
- `initializeNodeContext` calls `traceSnapshotFromValue(stateCopy)` for **every node** (`graph/executor.go:2873`, then 3659-3681: `safeClone` + `json.Marshal` of the entire state). It does this *before* `agent.StartExecutionTraceStep` checks whether trace capture is on (`agent/execution_trace.go:122-126`), and there is no option to turn it off. Node results are marshalled the same way (3036).
- The completion event deep-copies the final state (`graph/graph.go:549`), then deep-copies and `json.Marshal`s every key again (`graph/events.go:1662-1685`), **ignoring `DisableDeepCopy`** (1670).
- Profile at 1 MB: 94% of allocated bytes come from `marshalTraceSnapshot`.

**Workaround:** keep `conv` out of the state and hold it in `*run`. I measured this and did not keep it. Cost becomes flat at about 700-810 µs/op, 160 KB/op and 1725 allocs/op from 1 KB to 1 MB.

## 5. Results

**Tests:** `gofmt -l` prints nothing; `go vet` is clean. `go test -race -count=3` passed 3 of 3 invocations, 10 full suite passes in all including one `-v` run, with no flakes. Every subtest passes:

- direct
- council
- findings in researcher order
- hide deliberation
- seeds and jitter
- parallel width 3
- parallel width 8
- bounded loop
- cancel
- member failure cancels its siblings
- slow consumer loses nothing

Native mode also passes `-race -count=3`.

**Benchmark (default closure mode, idiomatic: `conv` in state):**
```
BenchmarkCouncil/council/state=1KB-12         	    3504	    892192 ns/op	  228779 B/op	    2262 allocs/op
BenchmarkCouncil/council/state=64KB-12        	     842	   2981363 ns/op	 1905789 B/op	    2284 allocs/op
BenchmarkCouncil/council/state=1024KB-12      	     100	  22531709 ns/op	33049792 B/op	    2388 allocs/op
BenchmarkCouncil/direct-12                    	    8737	    278297 ns/op	   65120 B/op	     608 allocs/op
BenchmarkCouncil/fanout/width=3-12            	      28	  81462769 ns/op	         8.000 peak-goroutines	         1.084 x-ideal
BenchmarkCouncil/fanout/width=8-12            	      28	  80850674 ns/op	        13.00 peak-goroutines	         1.073 x-ideal
BenchmarkCouncil/ttft-12                      	      22	 102301302 ns/op	     54723 council-first-content-µs	      4644 council-first-thinking-µs	      4650 direct-first-content-µs
```

Baseline on the same machine, same run: council 60-63 µs / 14 KB / 159 allocs at every state size; direct 2.65 µs / 28 allocs; fan-out x-ideal 1.008 / 1.021, peak goroutines 7 / 12; ttft thinking 4293 µs, content 53321 µs.

Compared with the baseline:
- about 15x slower on the 1 KB council, and 360x slower with 2300x the bytes at 1 MB;
- direct path about 100x slower;
- fan-out wall time about 7% above ideal, against 1-2% for the baseline;
- first thinking token about 0.35 ms later.

**Conv-out-of-state variant (measured, not kept):**
```
council/state=1KB 811185 ns/op 160287 B/op 1725 allocs/op
council/state=64KB 708907 ns/op 159575 B/op 1725 allocs/op
council/state=1024KB 702579 ns/op 159505 B/op 1724 allocs/op
direct 225432 ns/op 48212 B/op 465 allocs/op
```

**Native streaming run:**
```
council/state=1KB 990520 ns/op 250443 B/op 2534 allocs/op
council/state=1024KB 29806080 ns/op 33088044 B/op 2667 allocs/op
direct 270770 ns/op 69685 B/op 658 allocs/op
fanout/width=8 1.089 x-ideal
```

**Binary and dependencies:**
- `cmd` binary built with `-trimpath -ldflags='-s -w'`: **13,369,609 bytes**.
- `go list -deps`: **432** packages, of which **233** have a dot in the path; 43 of them are trpc-agent-go packages.
- Heavy modules linked even with tracing disabled:
  - `google.golang.org/grpc v1.70.0`, pulled in by `internal/telemetry`
  - `go.opentelemetry.io/otel{,/trace,/metric,/sdk} v1.36.0`
  - `otel/exporters/otlp/otlptrace{,/otlptracegrpc,/otlptracehttp} v1.29.0`, pulled in by `telemetry/trace`
  - `go.opentelemetry.io/proto/otlp`, `google.golang.org/protobuf`, `genproto` (api and rpc), `grpc-gateway/v2`
  - `go.uber.org/zap`, `multierr`, `go-logr`, `golang.org/x/net`, `x/text`, `yaml.v3`, `google/uuid`, `cenkalti/backoff/v4`, `trpc-a2a-go`

  That is 9 otel modules plus grpc, protobuf and genproto. `go get …/graph@v1.11.2 …/agent@v1.11.2` added 24 indirect requirements to go.mod and changed no existing versions.
- LOC: `trpcagent.go` is 301 lines (244 non-comment, non-blank). The baseline is about 70.

## 6. Friction, footguns and `any`

- **Unconditional per-node JSON of the whole state.** `graph/executor.go:2873` and `3659-3681` run even though trace capture is off, and there is no switch. This is the single biggest cost; it looks like a bug, since the result is discarded when capture is disabled.
- **Silent data loss in deep copy:**
  - funcs, chans and `unsafe.Pointer` become zero values (`graph/utils.go:721`);
  - unexported struct fields are skipped, so they come back zeroed (`graph/utils.go:816`).

  Put an `emit` func, or a struct with private fields, in the state without `DisableDeepCopy` or `DeepCopier` and it silently arrives as nil or empty.
- **`DisableDeepCopy` is not honoured by the final-state serializer** (`graph/events.go:1670`). A `*run` holding a model with a mutex and maps would be reflect-copied at completion unless it implements `DeepCopier`.
- **Errors are stringified** across the event channel (`graph/executor.go:1816-1861`). You need a side channel to keep `errors.Is`.
- **No sibling cancellation** inside a superstep (`graph/executor.go:2584-2625`).
- **`MaxConcurrency` defaults to GOMAXPROCS** and **`MaxSteps` to 100**. Both are executor-wide, not per request.
- **Fan-out cannot update global state.** A node returning `[]*Command` cannot also write the global state, so each fan-out needs an extra dispatcher superstep.
- **`any` everywhere:**
  - `NodeFunc` returns `any` and the result is type-switched (`graph/executor.go:4084-4125`);
  - `State` is `map[string]any`, read with `GetStateValue[T]` type assertions;
  - reducers are `func(any, any) any`;
  - `AddConditionalEdges(from, condFunc any, …)` type-switches and **panics** on an unsupported function type (`graph/graph.go:75-99`) instead of failing to compile;
  - results come back as JSON bytes in `StateDelta`;
  - custom event payloads are JSON-encoded `any`.
- **Log noise:** zap `WARN` with a full event dump on every event dropped after cancellation (`event/event.go:487`). It shows up on every cancelled request.
- **Runtime mutation of the compiled graph** in conditional edges and `GoTo` (`graph/executor.go:4336`, `4688`).
- **Surprises on the plus side:** BSP gives the fan-in join for free, `[]*Command` handles a runtime width cleanly, the cycle works as written, the shared executor was race-clean under concurrent mixed-width runs, and native streaming loses nothing and keeps per-member order.

## 7. Verdict for xollama's server hot path

Do not use it. It can express the council faithfully: a conditional-edge route, `[]*Command` fan-out with reducers, a real cycle, and one executor compiled once and safely shared. But on the request path it costs 0.2-0.9 ms of CPU and about 450-2300 allocations per request, against the baseline's 3-60 µs. That cost grows with conversation size at about 22 ms and 33 MB per request for a 1 MB conversation, because every node JSON-marshals its whole input state with no switch to turn it off.

It also stringifies errors, does not cancel siblings (I had to add both), and adds about 430 packages, gRPC and the OTLP exporters to the binary even with tracing disabled.

The model calls dominate wall time, so fan-out wall time only reaches 1.07-1.08x ideal, and the result is technically workable. But the overhead buys nothing the ~70-line errgroup baseline lacks: checkpoints, interrupts and visual graphs are not features the council needs. The `any`/JSON boundaries, the silent deep-copy data loss and the upstream tracing bug are ongoing maintenance risk. If a graph abstraction is wanted, this is a reference design to borrow from, not a dependency.
