---
paths:
  - llm/engine_free_at_load.go
  - llm/engine_free_at_load_test.go
  - llm/llama_server.go
  - discover/refresh_stale.go
  - discover/refresh_stale_test.go
  - discover/runner.go
  - server/xollama_delete_unload.go
  - server/xollama_delete_unload_test.go
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
- **A runner's number does not outlive the runner** (`stale-free` hook,
  `discover/refresh_stale.go`, hooked in `discover/runner.go`):
  `noteListedFree` records what a listing measured (`listedFree`, guarded by
  `deviceMu`); `settleStaleFree` puts a device no listing and no loaded
  runner measured back to the last listed value, at Warn — only when the
  caller passed its runners (`runners != nil`: the scheduler, whose list is
  empty, never nil, with nothing loaded). The devices route, the link probe
  and the startup log pass nil; settling for them showed a loaded model's
  card as empty in the table. Guards:
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
- Registry rows `free-at-load`, `stale-free`, `delete-unload` in
  `docs/protocols/UPSTREAM-SYNC.md`; prose in
  `docs/features/device-selection.md`.

```sh
go test ./llm/ -run 'FreeAtLoad|LastDeviceLine'
go test ./discover/ -run 'StaleFree|OutliveTheRunner'
go test ./server/ -run 'Delete|UnloadByAName'
```
