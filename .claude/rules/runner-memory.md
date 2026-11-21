---
paths:
  - llm/engine_free_at_load.go
  - llm/engine_free_at_load_test.go
  - llm/engine_overcommit.go
  - llm/engine_overcommit_test.go
  - llm/llama_server.go
  - discover/refresh_stale.go
  - discover/refresh_stale_test.go
  - discover/runner.go
  - server/xollama_delete_unload.go
  - server/xollama_delete_unload_test.go
  - server/load_log.go
  - server/routes.go
  - docs/features/device-selection.md
---

# A runner's memory, while it runs and after it is gone

- **Free at load is the first device line** (`free-at-load` hook,
  `recordFreeAtLoad` in `llm/engine_free_at_load.go`, called from
  `parseLineLocked` in `llm/llama_server.go`). A load with an assistant
  drafter prints `using device <dev> (...) - N MiB free` again after the
  target is on the card (22089 then 6875 MiB on a 3090); keeping the last one
  made `GetDeviceInfos` report 0 free and loads went to the 9070 XT
  (eleven2go, 2026-11-16, mails #2309/#2310). `XOLLAMA_ENGINE=llamacpp`
  keeps the last line. Guards: `TestFreeAtLoadIsTheFirstDeviceLine`,
  `TestStockEngineKeepsTheLastDeviceLine`.
- **A runner past its card is not 100% VRAM** (`engine-overcommit` hook,
  `llm/engine_overcommit.go`, one marked line in `MemorySize`): on opencoti,
  device buffers past the engine's first free-at-load line for a discrete GPU
  (`Overcommitted`) are taken off `size_vram`, and `logOvercommit`
  (`server/load_log.go`) says so at Warn after the load. Windows pages that
  part into shared memory without an error (mail #2362, `num_gpu 99` on the
  RX 9070 XT). Stock llama.cpp and integrated GPUs keep upstream's figures.
  Guards: `llm/engine_overcommit_test.go`.
- **A runner's number does not outlive the runner** (`stale-free` hook,
  `discover/refresh_stale.go`, hooked in `discover/runner.go`):
  `noteListedFree` records what a listing measured (`listedFree`, guarded by
  `deviceMu`); `settleStaleFree` puts a device no listing and no loaded
  runner measured back to the last listed value, at Warn — only when the
  caller passed its runners (`runners != nil`: the scheduler, whose list is
  empty, never nil, with nothing loaded). The link probe and the startup log
  pass nil and put nothing back. Guards:
  `TestARunnersFreeMemoryDoesNotOutliveTheRunner`,
  `TestACallerThatDoesNotKnowTheRunnersPutsNothingBack`,
  `TestTheRefreshSettlesStaleFreeMemory`.
- **A delete unloads the model first** (`delete-unload` hook,
  `server/xollama_delete_unload.go`): `s.unloadBeforeDelete(n)` in
  `DeleteHandler` before `manifest.RemoveNamed` (an idle runner waited for up
  to `deleteUnloadWait`, 30 s; a busy one unloads when its request ends), and
  `s.unloadOrphan(...)` at the head of the model-not-found case in
  `GenerateHandler` and `ChatHandler`, so keep_alive 0 for a name no longer
  on disk still expires its runner (mail #2307). Upstream unloads only from
  `ollama rm`; `XOLLAMA_ENGINE=llamacpp` keeps upstream's delete and 404.
  Guards: `TestDeleteHandlerUnloadsTheModel`,
  `TestDeleteOfABusyModelUnloadsWhenTheRequestEnds`,
  `TestUnloadByANameNoLongerOnDisk`, `TestStockEngineDeleteDoesNotUnload`.
- **The device table asks the loaded runners** (`device-select` route,
  `s.XollamaDevicesHandler` → `loadedRunners` in `server/identity.go`).
  Discovery is a cache the scheduler refreshes before a load and at an
  unload; without the runners the table showed a card's free memory from
  before the model loaded (eleven2go 3090: 22.8 GiB free under 15.1 GiB).
  Guard: `TestTheDevicesRouteAsksTheLoadedRunners`.
- Registry rows `free-at-load`, `engine-overcommit`, `stale-free`, `delete-unload` in
  `docs/protocols/UPSTREAM-SYNC.md`; prose in
  `docs/features/device-selection.md`.

```sh
go test ./llm/ -run 'FreeAtLoad|LastDeviceLine|Overcommitted|AllOnTheGPU|IntegratedGPUs'
go test ./discover/ -run 'StaleFree|OutliveTheRunner'
go test ./server/ -run 'Delete|UnloadByAName'
```
