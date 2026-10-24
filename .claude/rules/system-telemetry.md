---
paths:
  - internal/systel/**
  - server/xollama_system.go
  - server/xollama_system_models.go
  - server/xollama_system_kv.go
  - server/xollama_system_kv_test.go
  - server/xollama_system_test.go
  - server/xollama_linkprobe.go
  - discover/known_devices.go
  - api/xollama_system.go
  - docs/xollama/system.mdx
  - docs/features/gpu-metrics-sidecar.md
---

# Host, process and GPU telemetry (`system-telemetry` hook)

- `GET /api/xollama/system` is `SystemHandler` in `server/xollama_system.go`.
  The only upstream edit is one `r.GET(api.XollamaSystemPath, s.SystemHandler)`
  in `server/routes.go`, beside the devices route. Everything else is additive.
  Registry row `system-telemetry` in `docs/protocols/UPSTREAM-SYNC.md`.
- **One cached snapshot.** It refreshes every 2 s while someone has polled in
  the last minute, then stops. Every request gets the cached copy.
- **A request never starts anything.** No GPU discovery, engine, runner or
  link probe, and it never waits on a load. The scheduler is read with `TryLock`
  (`server/xollama_system_models.go`), and discovery's last list likewise
  (`discover/known_devices.go`). `server/xollama_linkprobe.go` only keeps each
  probe's result for the snapshot. Guard: `TestTheSystemRouteNeverWaitsOnALoad`
  in `server/xollama_system_test.go`, verified by mutation. The route test fails
  if discovery runs.
- **Read in process, never by running a tool** (no `nvidia-smi`). The readers
  are in `internal/systel/`. The host and the process tree come from `/proc`,
  Win32 or libproc. GPUs come from NVML loaded at run time (`nvml_linux.go`,
  `nvml_windows.go`), `/sys/class/drm` (`drm_linux.go`), D3DKMT plus the GPU
  performance counters (`d3dkmt_windows.go`), or IOKit (`iokit_darwin.go`).
  Platform files are split by build tag, with `platform_other.go` for the rest.
- **The Windows metrics sidecar only fills nils** (`xo-38`,
  `internal/systel/sidecar.go`, `docs/features/gpu-metrics-sidecar.md`).
  `sidecarReader` runs last in `platformReaders` (`platform_windows.go`) and
  reads `%LOCALAPPDATA%\xOllama\gpu-metrics.json` (version 1, matched by PCI ID
  or `integrated`). It never adds a device or overrides a reading. A missing file
  is silent. A stale file (`sidecarMaxAge`, 10 s), a malformed one or one with the
  wrong version is warned once. Guards: `TestSidecarEnrichesByPCIID`,
  `TestSidecarIgnoresStale`, `TestSidecarMissingFileIsSilent`.
- **An integrated GPU's memory is its shared pool.** `isIntegratedUMA`
  (`internal/systel/integrated.go`) uses `discover/amd.go`'s thresholds, so
  `/api/xollama/system` and `/api/xollama/devices` agree. An iGPU is marked
  `Integrated`, and its total is dedicated plus GTT (`drm_linux.go`) or plus
  SharedSystem (`d3dkmt_windows.go`) (bug-320). Guards: `TestIsIntegratedUMA`,
  `TestAMDReadingsIntegratedMemory`, `TestAMDReadingsDiscreteMemoryUnchanged`.
- **A model's KV report is cached apart from the snapshot** (`kv_host_telemetry_v1`,
  `server/xollama_system_kv.go`). The models are listed per request, so the
  engine's `GET /kv` (`rolling_kv`, opencoti patch 0481) is read in the
  background: `systemKV` returns what is cached and starts `refreshKV` at most
  once per `systemInterval`, bounded by `kvFetchTimeout`. Only a runner whose
  `EngineName()` is `opencoti` is asked; stock llama.cpp reports null and is
  never called. A failed read is null, never the last reading, and is warned
  once per runner. `forgetKV` drops unloaded runners. `kvFromEngine` keeps null
  apart from 0: the engine writes a 0 size and every KVarN size as null, so
  `host_bytes` is 0 from the counts (`!engaged || tail_cells == 0`) and null only
  for a tail with no size. Guards: `TestTheSystemRouteReportsTheEnginesKVWithoutWaitingOnIt`
  (both the stock skip and the background read verified by mutation),
  `TestTheEngineIsAskedOncePerInterval`, `TestAFailedReadIsNullNotTheLastReading`,
  `TestTheKVReportKeepsNullApartFromZero`.
- **A build without cgo still compiles.** The cgo readers (`nvml_linux.go`,
  `host_darwin.go`, `iokit_darwin.go`, `platform_darwin.go`) carry
  `//go:build cgo`. On Linux `nvml_linux_nocgo.go` returns no NVML, so NVIDIA
  GPUs are read from sysfs for their link only; macOS without cgo takes
  `platform_other.go`.
- **Off means off.** Nothing changes on the inference path or for
  `XOLLAMA_ENGINE=llamacpp`. Clients gate on `system_telemetry_v1` in
  `/api/xollama`'s features.

```sh
go test ./internal/systel/... ./server/ -run System
CGO_ENABLED=0 go build ./internal/systel/...
```
