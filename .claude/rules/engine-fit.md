---
paths:
  - llm/engine_fit_target.go
  - llm/engine_fit_target_test.go
  - llm/llama_server.go
---

# The fit target on opencoti

- Upstream adds the vision projector's size plus `mmprojOffloadHeadroom`
  (1 GiB) to `LLAMA_ARG_FIT_TARGET`. Its own comment calls this "a stopgap
  until fit accounts for mmproj memory directly" (ollama/ollama#16996). Stock
  llama.cpp keeps that padding; leave `extraEnvsForStart` as upstream wrote it.
- **On opencoti the padding is never added.** `opencotiEnvsForStart` in
  `llm/engine_fit_target.go` returns `launch.extraEnvs` unchanged and only logs
  the padding it skipped. opencoti's fit places the projector itself (patch
  0426). An explicit fit target turns the engine's automatic margin off, and
  the engine then holds the target out of the KV window.
- Measured on a V100 (2026-09-27): an 885 MiB projector plus 1024 MiB gave a
  1909 MiB margin. 4063 MiB of the KV window went to the host, 4.9 GB of VRAM
  sat unused, and generation ran at 2.9 tok/s.
- A fit target that the launch or the operator states is passed on unchanged.
  That is their choice. Never recompute it here.
- The `engine-fit` hook is one line in `llm/llama_server.go`, inside
  `if usedOpencoti`, right after upstream's `extraEnvsForStart`. Its row in
  `docs/protocols/UPSTREAM-SYNC.md` is `engine-fit`.
- Guard: `TestAVisionModelOnOpencotiGetsNoFitTarget`, next to upstream's
  `TestMMProjFitTargetExtraEnvs`.
