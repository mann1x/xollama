---
paths:
  - mlxrunner/**
  - mlx/**
  - cmake/**
  - manifest/manifest.go
  - manifest/opencoti_first.go
---

# MLX runner

- Architectures register in `mlxrunner/model/architectures/architectures.go`;
  each one lives under `mlxrunner/model/<name>/` with a sibling `_test.go`
  (see `mlxrunner/model/qwen3_5/` for the full shape: weights, vision, config).
- Reuse layers from `mlxrunner/nn/` (`linear.go`, `norm.go`, `rope.go`,
  `sdpa.go`, `recurrent.go`) and caches from `mlxrunner/cache/`. Do not hand-roll
  KV cache or rotating-window logic.
- Tokenizer changes belong in `mlxrunner/tokenizer/`; parity against GGML is
  asserted by `mlxrunner/tokenizer/tokenizer_ggml_parity_test.go`.
- `mlx/mlxtest` and `mlx/mlxthread/mlxthreadtest` are test-only — `depguard` in
  `.golangci.yaml` blocks importing them from non-test code.
- Build with `cmake -B build . -DOLLAMA_MLX_BACKENDS=cuda_v13`; versions are
  pinned in `MLX_VERSION` and `MLX_C_VERSION`.
- **A model opencoti can serve never runs on MLX, on any OS** (owner,
  2026-10-07). `runnerPreferences` in `manifest/manifest.go` calls
  `opencotiFirst` (`manifest/opencoti_first.go`, `opencoti-first` hook): a
  manifest list's `llamacpp`, then `ggml` children come before `mlx`, for a
  pull and a model on disk. MLX keeps only what has no GGUF child; an explicit
  `--runner` is untouched (`runnerPreferencesFor`); `XOLLAMA_ENGINE=llamacpp`
  gets upstream's order. Guard `manifest/opencoti_first_test.go`. Re-check any
  new runner-selection path against this rule on every sync.
- **The MLX runner opens only the MLX child of a manifest list.** `NewClient`
  (`mlxrunner/client.go`) and `Open` (`mlxrunner/model/root.go`) both call
  `manifest.ParseNamedManifestForRunner(..., manifest.RunnerMLX)`, never
  `ParseNamedManifest`: the platform order puts `llamacpp` first off Apple
  silicon, and with both children local a `runner=mlx` subprocess opened the
  GGUF (`config "config.json" not found in manifest`). Carried from the fork
  (`up-mlxrunner-manifest-list`); guard `mlxrunner/model/root_test.go`.
