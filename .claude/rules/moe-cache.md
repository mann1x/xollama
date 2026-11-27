---
paths:
  - llm/engine_moe_cache.go
  - llm/engine_graceful_stop.go
  - server/xollama_moe_cache.go
  - types/xollama/moe_cache.go
  - cmd/tweak/moe_cache.go
  - docs/xollama/moe-cache.mdx
---

# The MoE expert cache (`moe-cache`, `engine-graceful-stop` hooks)

- opencoti c14 r19 (`moe_cache_v1`, `moe_cache_auto_v1`, `moe_cache_state_v1`;
  mails #2397, #2399, #2401, #2403). **The engine decides by default**: xOllama
  passes no `-ngl`, opencoti defaults to `--fit on` and `--moe-strategy auto`,
  so every single-GPU MoE model that overflows its card gets the automatic
  cache when the engine measures it to pay, loaded `--no-mmap` (pinned RAM,
  counted against the Windows commit limit). Nothing in xOllama turns it on.
- **Model settings in `fit`** (`types/xollama/moe_cache.go`, owner's request
  via opencoti #2397): `moe_cache` = `auto` | `off` | MiB, `moe_cache_cpu_share`
  0..1, `moe_cache_warm_start`. Also server defaults (`fit` is in
  `DefaultSections`). **No schema floor**: every older build ships an engine
  with no cache, so dropping the fields changes nothing there
  (`TestTheMoECacheRaisesNoSchemaVersion`). Refused: a share or warm start under
  `off`, `auto` with the fit off or another strategy, any of them on a model
  pinned to llamacpp.
- **Launch** (opencoti only): a size is `--moe-cache-mib`, a share
  `--moe-cache-cpu-share` (`appendMoECacheArgs` from `appendEnginePolicyArgs`);
  `off` is `OPENCOTI_MOE_CACHE_AUTO=0`, a stated `auto` is `=1` (the model beats
  an operator's 0), warm start off is `OPENCOTI_MOE_CACHE_PROFILE=0`
  (`withMoECacheEnvs`, one hook line after `WithVulkanCache`). Refused by name on
  a pin without `moe_cache_v1` (`enginePolicyRefusal`).
- **The verdict** is `/props` `opencoti.devices[].moe_cache`, read per `ps`
  (`MoECacheState`, `psMoECache`) into `ProcessModelResponse.MoECache` and
  printed under the `ps` table (`tweak.MoECacheText`; `off` prints nothing).
- **Forget the warm-start profile**: `POST /api/xollama/moe-cache/forget`
  (`MoECacheForgetHandler`), `xollama tweak model <m> --forget-moe-profile`.
  It unloads the model (busy: 409), then deletes
  `<payload root>/.llamafile/moe-cache/<key>.mcp` in every
  `engine.DefaultPayloadRoots`. The key is opencoti's `profile_init`: FNV-1a 64
  over `general.name` ("n/a" if absent), then block_count, expert_count and the
  sum of every tensor's elements as three LE u64 — **with the engine's offset
  basis `1469598103934665603`**, the standard one missing its last digit.
  `hash/fnv` gives another key (`moeKeyOffset`; checked against the file r19
  wrote: Qwen3.6 27B A3B Coder → `f335783fc163c1be`). An engine update purges
  the whole `.llamafile` tree, profiles included.
- **The profile is only written on a clean exit**, so `stopProcess` asks an
  opencoti engine to `POST /shutdown` first and waits up to 10 s
  (`engine-graceful-stop`, `graceful_shutdown_v1`; planned since 2026-10-04,
  0555). Before it, xOllama's SIGKILL meant no profile was ever written.
- Known at r19 (2026-11-27, mail #2411): the automatic cache plus an attached
  NextN head segfaults in the auto-MTP warm-up (exit 139) on the release bytes;
  either alone loads. An engine fix, never a workaround here.

```sh
go test ./llm/ -run 'MoECache|ShutDown|DoesNotExit|PCIAddress'
go test ./server/ -run 'ProfileKey|ForgettingTheProfile'
go test ./types/xollama/ -run MoECache
go test ./cmd/tweak/ -run MoECache
```
