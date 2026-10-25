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

The installer puts it beside `xollama.exe`, and the server runs it while
`/api/xollama/system` is polled, with `--no-cpu --parent <pid>`, and stops it
when the polling stops (`internal/systel/sidecar_run_windows.go`). Run by hand,
it is the same program:

```
xollama-gpu-sidecar [--out PATH] [--interval SECONDS] [--once] [--no-cpu] [--parent PID] [--dump]
```

- `--out`       output file (default `%LOCALAPPDATA%\xOllama\gpu-metrics.json`)
- `--interval`  seconds between writes (default 2)
- `--once`      write one snapshot and exit (smoke test)
- `--no-cpu`    do not open the CPU (skips the Ryzen SMU; no admin needed)
- `--parent`    exit when that process does
- `--dump`      print every GPU hardware + sensor name/value and exit (debugging)

The file is written atomically (temp + rename; the rename is tried again while
the server holds the file open, and a write that still cannot land is skipped
until the next interval). Each write advances `updated_unix`; the server treats
a file older than 10 s as stale.

## Build

Needs the .NET SDK (8.0+) and, on the build machine, a .NET runtime at least
as new as the pinned one (8.0.32, or any 9 or 10), because the pinned trimmer
runs on it. The project builds one self-contained, partially trimmed Windows
exe (no SDK or runtime on the target, 15.9 MiB). Every package version comes
from `packages.lock.json`, and the runtime it carries and the trimmer are
pinned in the `.csproj` (`RuntimeFrameworkVersion`, `SidecarILLinkVersion`),
so any SDK builds the same bytes:

```
dotnet publish -c Release -p:RestoreLockedMode=true -o out
```

`scripts/build_windows.ps1 gpuSidecar` runs exactly that and copies the exe to
`dist\windows-amd64\`. After changing a package version or the two pins, run
`dotnet restore --force-evaluate` and commit the new lock file. `THIRD-PARTY-NOTICES.txt`
ships beside the exe as `xollama-gpu-sidecar-NOTICES.txt`.
