---
paths:
  - llm/engine_args.go
  - llm/engine_args_test.go
  - llm/llama_server.go
  - docs/xollama/engine-args.mdx
---

# Operator argv passthrough

- `XOLLAMA_ENGINE_ARGS` is the escape hatch for engine flags with no
  `LLAMA_ARG_*` twin (`--override-kv` above all). `appendEngineArgs` in
  `llm/engine_args.go` splits it and appends it verbatim; the `engine-args`
  hook is one call in `startLlamaServer` (`llm/llama_server.go`), after every
  other append.
- **Appended last, on purpose.** `llama-server` takes the final occurrence of a
  repeated flag, so last-wins puts the operator ahead without this code
  reordering or removing anything. Never move the call earlier.
- Unset, it returns the argv slice untouched and logs nothing — that is what
  keeps the `XOLLAMA_ENGINE=llamacpp` path byte-identical to upstream.
- **Operator-side only, and not "not yet".** It is never read from a model:
  `types/xollama` is not involved and must not become involved. A model that
  could append to `llama-server`'s argv could act on the machine that pulled
  it. Model-side settings stay named fields with checked values.
- `guardedEngineFlags` refuses by name, in both the `--flag value` and
  `--flag=value` spellings and in both short and long forms: `-c`/`-np`/`-ngl`/
  `-b`/`-ub` feed the scheduler's VRAM estimate, `-m`/`--host`/`--port` are how
  the server addresses and reuses the runner, `-lv`/`--verbosity`/
  `--log-disable`/`--log-file` are how it reads the load. Adding a flag xollama
  starts setting means adding its guard row in the same commit.
- The splitter treats a backslash as a literal backslash, never an escape —
  Windows paths are a likelier value than an escape sequence. Quote instead.
- What is appended is logged at INFO, not DEBUG.
- Registry row `engine-args` in `docs/protocols/UPSTREAM-SYNC.md`; the env var
  lives in `envconfig/config.go` **and** its `AsMap()` row. Prose for users is
  `docs/xollama/engine-args.mdx`; the gap it closes is G2 in
  `docs/features/opencoti-config-gaps.md`. Cover changes in
  `llm/engine_args_test.go`.
