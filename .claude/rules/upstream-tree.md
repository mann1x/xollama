---
paths:
  - server/**
  - llm/**
  - api/**
  - cmd/**
  - envconfig/**
  - discover/**
  - fs/**
  - middleware/**
  - decision/**
---

# Upstream tree — additive files or marked hooks

- Prefer a new file over editing an upstream one. When an edit is unavoidable,
  wrap it in a marked surgical hook and add it to the Registry in
  `docs/protocols/UPSTREAM-SYNC.md` in the same commit.
- Imports stay `github.com/ollama/ollama/...`. Never rewrite the module path.
- Gate fork behaviour so `XOLLAMA_ENGINE=llamacpp` is byte-identical to
  upstream. New env vars go in `envconfig/config.go` **and** in `AsMap()` so
  `ollama serve --help` documents them.
- New routes go in `server/routes.go` with coverage in `server/routes_test.go`;
  scheduler changes need `server/sched_test.go`.
- Pool ids on the `engine-session` hook are `*int` — `PoolID` in `llm/server.go`,
  `llm/llama_server.go`, `poolFor` in `llm/engine_pool.go` and the `council`
  hook's `Placement.PoolID` (`placementFields` in `llm/engine_council.go`). The engine numbers
  its first pool `0`, so `nil` means "no pool"; a `> 0` guard or an `omitempty`
  plain `int` on the wire silently drops pool 0. See `docs/xollama/sessions.mdx`.
- **A fallback is loud.** A path that degrades says why at Warn, or fails with
  the cause, never Debug: a device listing that fails carries the engine's last
  lines (`outputTail`, `discover/opencoti.go`), a refresh that finds nothing
  names the cooldown, or the 3 s budget it ran out of (`forkRefresh` in
  `discover/refresh_opencoti.go` lists the backends side by side inside it). Guard:
  `discover/opencoti_loud_test.go`. A backend is only listed where it can exist
  (`existsOn`, `discover/opencoti.go`: Metal on macOS only, and nothing else
  there), even with `XOLLAMA_ENGINE=opencoti`. Guard:
  `discover/opencoti_backends_test.go`. Discovery reads the platform through
  `opencotiHost` (`discover/opencoti.go`), so a test about CUDA or Vulkan sets
  the platform it is about (`discover/opencoti_test.go`,
  `discover/opencoti_cuda12_test.go`) and runs the same on a macOS runner.
- **An engine's refusal is an answer, not a failed listing.** A listing in
  which opencoti refused every device (`ggml_vulkan: <name> (driver <x>) is
  REFUSED: …`, since c10) drops that backend's devices at Warn
  (`withRefusals` / `isRefusedListing`, `discover/opencoti_refused.go`), never
  falls back to llama.cpp's view: that placed models on the refused device and
  every load failed. Guard: `discover/opencoti_refused_test.go`.
- Run `golangci-lint run` before pushing — `.golangci.yaml` uses `gofumpt` and a
  `depguard` rule denying `internal/testutil` outside `_test.go` files.
