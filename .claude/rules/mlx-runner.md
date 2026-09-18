---
paths:
  - mlxrunner/**
  - mlx/**
  - cmake/**
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
