# xollama

A soft fork of [ollama](https://github.com/ollama/ollama) that carries the
fixes upstream has not merged, and serves GGUF models on the
[opencoti-llamafile](https://huggingface.co/ManniX-ITA/opencoti-llamafile)
engine.

> **Status.** `main` is upstream `v0.34.2` with all twelve carried patches
> replayed onto it — see
> [`docs/protocols/CARRIED-PATCHES.md`](docs/protocols/CARRIED-PATCHES.md).
> The engine swap is implemented and has been run against a live engine:
> routing and policy, the pinned artifact and its build-time fetch, argv
> translation, dynamic slots, engine sessions, shared prefix pools and the
> `XOLLAMA_ENGINE_ARGS` argv passthrough. The
> pinned engine is opencoti c7 r2 (`llm/engine/pin.txt`); a `dev` branch pins a
> development snapshot for integration testing. With `XOLLAMA_ENGINE=llamacpp`
> and no xollama flags the launch is byte-identical to upstream, which is what
> keeps the A/B honest. Measurements are in
> [`docs/evaluations/`](docs/evaluations/) and the designs in
> [`docs/features/`](docs/features/).

## Why

Good fixes sit in upstream PRs for months. Some of them are not cosmetic:

- **[#17566](https://github.com/ollama/ollama/pull/17566) — a thinking token
  budget.** Open since 2026-08-04. Without it there is no way to bound a
  reasoning model, and on a long agentic run the difference is a turn that
  finishes versus one that grinds to the token cap.
- **`/api/tokenize` and `/api/detokenize`.** ollama exposes no way to count
  tokens against the model actually loaded, so every client guesses. The
  oldest PR for it ([#12030](https://github.com/ollama/ollama/pull/12030)) has
  been open since 2025-08-22 — over a year — under a queue of "merge please"
  comments. The plumbing already exists internally; only the routes are
  missing.
- Eleven more parser and tool-call correctness fixes, listed in
  [`docs/protocols/CARRIED-PATCHES.md`](docs/protocols/CARRIED-PATCHES.md).

xollama is where those live in the meantime. Every one of them stays an open
PR upstream, and each is **retired from this fork the day upstream takes it**.

## Drop-in

Same API, same models directory, same clients. The binary is renamed and the
listen address moves to **22434** so an accidental collision with a stock
install is not possible — which is not the same as supporting the two side by
side: one model store with two writers and one GPU with two schedulers are
still one of each. See [`docs/features/rebrand.md`](docs/features/rebrand.md)
and [`docs/xollama/default-port.mdx`](docs/xollama/default-port.mdx).

## The engine

ollama 0.34 already runs GGUF models as a `llama-server` subprocess, so the
engine is one binary path and one argv behind an HTTP API. xollama routes
that to opencoti-llamafile — upstream llamafile plus a patch series aimed at
multi-session agentic serving on a fixed VRAM budget: KV quantization tiers,
a rolling-KV window, shared KV pools, DCA long context, MTP speculative
decode.

Routing is by *tested* platform and backend — CUDA, Vulkan and CPU go to
opencoti; ROCm and anything unvalidated stay on stock `llama-server`; macOS
keeps ollama's MLX path untouched. `XOLLAMA_ENGINE=opencoti|llamacpp|auto`
overrides it, which also makes an honest A/B possible against vanilla.

Design: [`docs/features/engine-opencoti-llamafile.md`](docs/features/engine-opencoti-llamafile.md).

## Settings that belong to the model

Which cache type a model tolerates, whether flash attention helps or breaks it,
how many conversations it should serve at once — none of that changes when you
move the model to another machine, and all of it changes when you swap the
model. ollama's settings for these are environment variables, which are
server-wide by construction.

xollama gives the model its own say, in a layer a stock ollama carries through
`push` and `pull` and ignores, so a model configured here still runs there:

```sh
xollama tweak model qwen3.6:latest          # walk every setting
xollama tweak model qwen3.6:latest --dca    # just this feature
xollama tweak model qwen3.6:latest --dca=on # no questions at all
```

[`docs/xollama/model-settings.mdx`](docs/xollama/model-settings.mdx) is the
field list, [`docs/xollama/tweak.mdx`](docs/xollama/tweak.mdx) is the command.

## Staying close to upstream

This fork is meant to be re-syncable indefinitely, so it has rules about what
may be changed and where:
[`docs/protocols/UPSTREAM-SYNC.md`](docs/protocols/UPSTREAM-SYNC.md).
`main` carries the full upstream history — 5771 commits — so every future
`git merge upstream/main` has a real merge-base.

## Upstream

For what ollama is and how to use it, see
[ollama/ollama](https://github.com/ollama/ollama) and
[ollama.com](https://ollama.com). xollama is not affiliated with or endorsed
by Ollama.

## Licence

MIT, unchanged from upstream — see [`LICENSE`](LICENSE). Copyright (c) Ollama,
plus xollama's contributors for the changes in this fork.
