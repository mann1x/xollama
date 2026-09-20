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
  `llm/llama_server.go` and `poolFor` in `llm/engine_pool.go`. The engine numbers
  its first pool `0`, so `nil` means "no pool"; a `> 0` guard or an `omitempty`
  plain `int` on the wire silently drops pool 0. See `docs/xollama/sessions.mdx`.
- Run `golangci-lint run` before pushing — `.golangci.yaml` uses `gofumpt` and a
  `depguard` rule denying `internal/testutil` outside `_test.go` files.
