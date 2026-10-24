# GPU metrics sidecar (`xo-38`)

`/api/xollama/system` (`system-telemetry` hook) reads every GPU **in process**,
from the vendor's own interface, and never runs a tool. That is complete on
Linux and for NVIDIA everywhere, but leaves a gap on Windows for AMD and Intel.

## What each reader gives today

| Reader | Metrics populated |
|---|---|
| `nvml.go` (NVIDIA, any OS) | mem, util, mem-util, temp, power, power-limit, clock, clock-max, fan, PCIe link |
| `drm_linux.go` (AMD/Intel, Linux) | util, mem, clock, temp, power, power-cap, fan, PCIe link — via amdgpu/i915 sysfs hwmon |
| `d3dkmt_windows.go` (AMD/Intel, Windows) | **mem + util only** |
| `iokit_darwin.go` (Apple) | util, used-mem |

D3DKMT (the kernel graphics interface behind Task Manager's graphs) exposes
memory and per-engine utilization, but **not** temperature, power, clocks, fan
or PCIe link. Those live in the vendor's own libraries, which are C++/COM or
.NET and, for an APU, behind a kernel driver — none of which belongs inside the
Go server. So a small external **sidecar** reads them and writes a file the
server merges in.

`api.SystemDevice` already carries every field (`TempC`, `PowerW`,
`PowerLimitW`, `ClockMHz`, `ClockMaxMHz`, `FanPct`, `Link`, `MemUtilPct`,
`Integrated`); the sidecar only has to fill them.

## The server side (done, `internal/systel/sidecar.go`)

`sidecarReader` runs **last** in the Windows reader chain, after `d3dkmt`, so it
only enriches devices the platform already enumerated:

- It **never adds a device** and **never overrides** a reading the platform
  made (a value the driver reported is authoritative; the sidecar fills nils).
- It reads `%LOCALAPPDATA%\xOllama\gpu-metrics.json` (beside the app's state,
  `app-state` hook). A **missing** file is the ordinary no-sidecar state and is
  silent; a file that is **present but stale, malformed or the wrong version**
  is logged once at Warn (something that should feed readings has stopped).
- Stale = older than `sidecarMaxAge` (10 s), from the file's `updated_unix`.
- Telemetry only: nothing here touches the inference path, and clients gate on
  `system_telemetry_v1`.

Tests: `TestSidecarEnrichesByPCIID`, `…IntegratedWithoutPCI`, `…IgnoresStale`,
`…MissingFileIsSilent`, `…MalformedIsReported`.

## The file contract (version 1)

```json
{
  "version": 1,
  "updated_unix": 1730000000,
  "gpus": [
    {"pci_id":"0000:03:00.0","name":"Radeon RX 7900 XTX","temp_c":55,"power_w":120,
     "power_limit_w":327,"clock_mhz":2400,"clock_max_mhz":2860,"fan_pct":38,
     "link":{"gen":4,"width":16,"max_gen":4,"max_width":16}},
    {"integrated":true,"name":"AMD Radeon(TM) Graphics","temp_c":48,
     "clock_mhz":2000,"clock_max_mhz":2200,"power_w":15}
  ]
}
```

- Each entry is matched to a device by **`pci_id`** (same `0000:bb:dd.f` form the
  server uses); an entry with **no** `pci_id` and `"integrated":true` matches the
  device flagged `Integrated` (an APU's graphics, which the SMU reports without a
  bus address); `name` is the last-resort match.
- Every metric field is optional — send what the source has. `updated_unix` must
  advance each write.
- The sidecar must write **atomically** (write a temp file, rename over) so the
  server never reads a half-written file.

## The sidecar (`tools/gpu-sidecar`)

A small C#/.NET console process (source in this repo), published self-contained
so it runs with no SDK or runtime on the target, writing the file every 1–2 s.
One dependency does nearly everything:

- **temp / power / clocks / fan / utilization** — LibreHardwareMonitorLib (AMD
  through ADL, NVIDIA through NVAPI/NVML, Intel). The AMD readings, an APU's
  integrated graphics included, come through ADL with **no admin and no kernel
  driver**.
- **PCIe link (negotiated + max) and the PCI bus id** — the Windows device
  property store via SetupAPI (below).

Running it elevated (the default unless `--no-cpu`) also opens the Ryzen SMU
through LibreHardwareMonitor's PawnIO driver, for finer APU detail (per-core/SoC
rails); nothing above needs it. Off by default: no file, no effect. Packaged
with the app, a user who installs xOllama gets the metrics; without it the
server behaves exactly as before.

## PCIe link

The negotiated link is already exposed to user code: the PCI bus driver
publishes the PCIe Link Status register as device properties, and SetupAPI reads
them in **user context** — no admin, no driver, no PawnIO. The sidecar reads,
per display adapter (`tools/gpu-sidecar/SetupApi.cs`):

- `DEVPKEY_PciDevice_CurrentLinkSpeed` / `CurrentLinkWidth` (pids 9/10) →
  negotiated `link.gen` / `link.width`
- `DEVPKEY_PciDevice_MaxLinkSpeed` / `MaxLinkWidth` (pids 11/12) → the card's
  `link.max_gen` / `link.max_width`

Both are reported, as osync already shows for NVIDIA. It is the *negotiated*
link, not a card maximum: the 3090 on eleven2go, in an x16 slot routed x4 through
the chipset, reads `gen4 x4` with `max gen4 x16`. The idle-downgraded transient
(a card parks to gen1 at idle) is **not** reported — that is what the wrong
property pids (7/8) returned. The same SetupAPI walk yields each GPU's PCI bus
id, set as `pci_id` so the server matches on the bus address, not the name.

The measured *bandwidth* (GB/s) is separate and still comes from opencoti's link
probe via `withMeasuredLink` (`server/xollama_system.go`).

## Status

Server side done and tested (above). The sidecar (C#/.NET 8,
LibreHardwareMonitorLib 0.9.4) is developed on pandorum at
`C:\Users\manni\source\repos\xollama-gpu-sidecar`, published self-contained
(single `win-x64` exe, no SDK/runtime needed on the target).

**Validated on eleven2go** (RTX 3090 + 9070 XT + Radeon iGPU), GPU-only, no admin:
all three GPUs read, names and the `Integrated` flag matching the server's own
`d3dkmt` names, so the merge connects once the build is deployed. The ADL path
gives the AMD readings **without** the SMU/PawnIO ring0 driver. Confirmed sensor
mapping from a `--dump`:

| field | AMD iGPU sensor | AMD 9070 XT sensor |
|---|---|---|
| temp_c | `Temperature GPU VR SoC` (no die edge) | `Temperature GPU Core` |
| power_w | `Power GPU Core` (GFX; `GPU SoC` is separate) | `Power GPU Package` |
| clock_mhz | `Clock GPU Core` (600) | `Clock GPU Core` — **0 when idle-gated** (reads under load) |
| fan_pct | — (fanless) | `Control GPU Fan` |
| util_pct | `Load GPU Core` | `Load GPU Core` |

No `power_limit` sensor on either AMD GPU, and no PCIe-link *sensor* in LHM (only
a "GPU Bus" load % on NVIDIA) — so the link comes from SetupAPI instead (above),
which read correctly on eleven2go: iGPU `gen4 x16` / max `gen4 x16`, 9070 XT
`gen5 x16` / max `gen5 x16`, 3090 `gen4 x4` / max `gen4 x16`. Each GPU's `pci_id`
is set from the same walk.

Still to do: run the sidecar as a loop writing the file; deploy the new xollama
build on eleven2go for the live end-to-end `/api/xollama/system` merge; optional
SMU extras (admin); and packaging with the app.

## Linux

Linux already reports AMD temp/power/util/clock/PCIe-link through amdgpu sysfs,
including an APU's integrated graphics (verified on a Cezanne 5600G: 48 °C edge,
42 W, Gen3/Gen4 link), so no sidecar is needed. The only thing sysfs does not
give is the SMU's finer detail (per-core/SoC rails), which the **`ryzen_smu`**
kernel module exposes at `/sys/kernel/ryzen_smu_drv/` (community module;
irusanov contributed but is not the author). The same `sidecarReader` can be
wired into the Linux chain later if that detail is wanted — it is generic — but
it is not registered there today, since amdgpu already covers the useful
readings.
