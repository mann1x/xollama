# xollama-gpu-sidecar

A small Windows helper that fills the GPU readings xollama's server cannot get
in process, and writes them to the file `/api/xollama/system` merges in.

On Windows the server reads AMD/Intel GPUs through D3DKMT, which exposes only
memory and utilization — no temperature, power, clocks, fan or PCIe link. This
sidecar reads those and writes
`%LOCALAPPDATA%\xOllama\gpu-metrics.json`; the server's `sidecarReader`
(`internal/systel/sidecar.go`) merges them, filling only fields it left nil and
never adding a device. See `docs/features/gpu-metrics-sidecar.md` for the
contract and the server side.

## What it reads

- **Temperature, power, clocks, fan, utilization** — via
  [LibreHardwareMonitorLib](https://github.com/LibreHardwareMonitor/LibreHardwareMonitor)
  (AMD through ADL, NVIDIA through NVAPI/NVML, Intel). The AMD GPU readings,
  including an APU's integrated graphics, come through ADL with **no admin and
  no kernel driver**.
- **PCIe link (negotiated + max) and the PCI bus id** — via the Windows device
  property store (`DEVPKEY_PciDevice_CurrentLinkSpeed`/`CurrentLinkWidth` and
  `MaxLinkSpeed`/`MaxLinkWidth`), which the PCI bus driver publishes from the
  PCIe Link Status register. This is a **user-context** read (what Device
  Manager shows) — no admin, no PawnIO. It is the *negotiated* link: a card in
  an x16 slot routed x4 through the chipset reports `gen4 x4` with `max gen4
  x16`. The idle-downgraded transient link is deliberately not reported.

LibreHardwareMonitor can optionally read the Ryzen SMU (per-core/SoC rails) via
its PawnIO driver when run elevated (`--cpu`, the default when not `--no-cpu`);
that needs admin. None of the readings above require it.

## Usage

```
xollama-gpu-sidecar [--out PATH] [--interval SECONDS] [--once] [--no-cpu] [--dump]
```

- `--out`       output file (default `%LOCALAPPDATA%\xOllama\gpu-metrics.json`)
- `--interval`  seconds between writes (default 2)
- `--once`      write one snapshot and exit (smoke test)
- `--no-cpu`    do not open the CPU (skips the Ryzen SMU; no admin needed)
- `--dump`      print every GPU hardware + sensor name/value and exit (debugging)

The file is written atomically (temp + rename). Each write advances
`updated_unix`; the server treats a file older than 10 s as stale.

## Build

Needs the .NET SDK (8.0+). Self-contained single exe (runs with no SDK/runtime
on the target):

```
dotnet publish -c Release -r win-x64 --self-contained true -p:PublishSingleFile=true -p:IncludeNativeLibrariesForSelfExtract=true
```

The exe lands in `bin/Release/net8.0/win-x64/publish/xollama-gpu-sidecar.exe`.
