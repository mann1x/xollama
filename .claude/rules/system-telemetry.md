---
paths:
  - internal/systel/**
  - server/xollama_system.go
  - server/xollama_system_models.go
  - server/xollama_system_test.go
  - server/xollama_linkprobe.go
  - discover/known_devices.go
  - api/xollama_system.go
  - docs/xollama/system.mdx
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
