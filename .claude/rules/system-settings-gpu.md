---
paths:
  - types/xollama/gpu.go
  - server/xollama_gpu.go
  - server/xollama_gpu_test.go
  - server/xollama_settings.go
  - server/xollama_linkprobe.go
  - server/xollama_linkprobe_test.go
  - server/sched.go
  - llm/engine_device_envs.go
  - llm/engine/linkprobe.go
  - llm/llama_server.go
  - api/xollama_settings.go
  - api/xollama_linkprobe.go
  - cmd/tweak/gpu.go
  - cmd/tweak/gpu_test.go
  - cmd/tweak/server.go
  - cmd/tweak/show.go
---

# The server's GPU policy (`system-settings`, `gpu` section)

- The policy is `xollama.GPUSettings` in `types/xollama/gpu.go`, kept in the
  settings file's `gpu` section and read by `serverGPU()` in
  `server/xollama_gpu.go` through `envconfig.SettingsSection`. It is decoded
  strictly (`jsonUnmarshalStrict`): a policy with an unknown field or one that
  fails `Validate` is ignored with a warning, never fatal.
- **Off means off.** With no policy every function returns what it was given
  and the scheduler is upstream's. Guard: `TestNoGPUPolicyChangesNothing`.
- **A model's own device pin wins.** `applyGPUPolicy` skips the disabled and
  backend filters for a pinned model (`cfg.Devices`); it still orders the GPUs
  highest priority first.
- GPUs are keyed by PCI ID (`CanonicalPCIID`). A GPU not listed is allowed, at
  priority 0, on its first backend. `preferredBackend` keeps a GPU on another
  backend only when its chosen one is not present.
- **Split:** `schedSpread()` replaces `envconfig.SchedSpread()`. `spread`
  always splits, `single` never does (`neverSplit()`: the rest of the layers
  stay on the CPU), `auto` or unset keeps `OLLAMA_SCHED_SPREAD`'s meaning only
  when unset. `split_mode` is `layer` or `row`; `tensor` is not offered
  (opencoti #609), and `Validate` refuses a `split_mode` with `single`.
- **Engine variables come through `llm.DeviceEnvs`** (`llm/engine_device_envs.go`),
  set in `init` to `gpuPolicyEnvs`: `LLAMA_ARG_SPLIT_MODE` only for a load on
  more than one GPU, and `OPENCOTI_LINK_GBPS` as one `<pci>=<GB/s>` entry per GPU of
  the load with a forced link (opencoti patch 0512, dev build 2610020826001
  and later; no gate, nothing before it was released). A GPU without one is
  left out and probed. `llm` never imports the policy; nil adds nothing.
- Hooks carry `// xollama-hook: system-settings`: in `server/sched.go`,
  `applyGPUPolicy` before `opencotiPlacement` in `processPending`,
  `schedSpread()` and `neverSplit()` in `selectLlamaServerPlacement`,
  `priorityOrder` first in `betterPlacementGPU`; in `llm/llama_server.go`,
  `DeviceEnvs(gpus)` merged into the launch's envs. Registry row
  `system-settings` in `docs/protocols/UPSTREAM-SYNC.md`.
- **Written only through `/api/xollama/settings`.** `SettingsRequest.GPU`
  replaces the section whole, an empty one clears it; `SettingsHandler`
  validates it first (400). `SettingsResponse.GPU` reports it.
- **The link probe** is `POST /api/xollama/link-probe`
  (`api.XollamaLinkProbePath`, `LinkProbeHandler` in
  `server/xollama_linkprobe.go`; the route line in `server/routes.go`), loopback
  only and not through a proxy. It runs `engine.LinkProbeCommand`
  (`llm/engine/linkprobe.go`: `--server --link-probe --gpu`, opencoti b62 or
  later) once per CUDA / Vulkan backend with `envconfig.Environ()`, bounded by
  `linkProbeTimeout`. A backend with a model generating on it (`generatingOn`)
  is not probed: it shares the link and the reading comes out low; an idle
  loaded model does not matter. A failure is that backend's `Error`
  (`linkProbeError`: the engine's own reason), never a failed request. Tests
  replace `linkProbeRun`.
- CLI: `xollama tweak server gpu [PCI-ID]` (`gpuCommand` in `cmd/tweak/gpu.go`,
  added in `cmd/tweak/server.go`; flags `--priority`, `--backend`, `--link`,
  `--split`, `--split-mode`, `--disable`, `--clear`, `--yes`).
  `xollama tweak show server` prints it with `printGPUTable`. Only the menu
  probes (`probeLinks`): `printGPUTable` adds the `DETECTED` and
  `MEASURED H2D` columns when given a `linkInfo`, nil elsewhere, and `askLink`
  starts from the detected PCIe generation and lanes. A server that cannot
  probe gives no link columns; the menu still works.
- Guards: `server/xollama_gpu_test.go`
  (`TestTheGPUPolicyDisablesOrdersAndPicksABackend`,
  `TestPriorityBeatsFreeMemoryForASingleGPU`,
  `TestSingleNeverSplitsAndSpreadAlwaysDoes`,
  `TestEachGPUOfALoadGetsItsOwnForcedLink`, `TestABadGPUPolicyIsRefused`),
  `server/xollama_linkprobe_test.go` (`TestTheLinkProbeReportsEachBackend`,
  `TestTheLinkProbeIsLocalOnly`, `TestABackendWithAModelGeneratingIsNotProbed`,
  `TestALinkProbeFailureSaysTheEnginesReason`) and `cmd/tweak/gpu_test.go`
  (`TestTheLinkWizardStartsFromWhatWasDetected`). Plan
  `plans/system-settings.md`; user prose in `docs/xollama/tweak.mdx`.
