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

## The sidecar (to build on the host — not in this repo)

A single lightweight process that writes the file every 1–2 s:

- **Discrete AMD board** (temp/power/clocks/fan + PCIe link): **AMD ADLX** (modern)
  or **ADL** (legacy) SDK. PCIe link gen/width also come from ADLX.
- **APU integrated graphics** (GFX clock, GFX/edge temp, SoC power): the **Ryzen
  SMU power-management table**. ADLX does not cover an APU's GFX well; the SMU
  does. **ZenStates-Core** (irusanov) reads the PM table and now uses **PawnIO**
  — a signed kernel driver with sandboxed Pawn modules, the path
  LibreHardwareMonitor migrated to, which avoids the unsigned-WinRing0 problem.
  The owner already runs ZenStates + PawnIO, so the sidecar wraps that.
- **Intel** (optional): LibreHardwareMonitorLib covers Intel GPUs too.

Keep it off by default: no file, no effect. Package it with the app so a user
who installs xOllama gets the metrics; a user without it sees exactly today's
behaviour.

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
