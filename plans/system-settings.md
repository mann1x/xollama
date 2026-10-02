# Server settings without environment variables

**Status:** ACTIVE. Phases 0–6 closed 2026-10-02, and the per-device link
form is wired. Open: a live opencoti run, waiting on eleven2go; the desktop
app restarting its server for a restart-needed setting. **Owner:** xollama.

## 1. What the owner asked for (2026-10-02, condensed)

- `xollama tweak server` today sets only the API key. It should set **every
  server-wide setting**, as real configuration in the same way as the API key.
  Environment variables stay supported, but they are impractical to change:
  you edit the global environment or a systemd override, look up the
  documentation, and restart.
- `xollama tweak envs`: overrides for the environment variables, so the
  operator can steer xollama without touching the global environment.
- `tweak server` sets the server's **defaults** for every pertinent
  `tweak model` setting:
  - the K/V cache type;
  - elastic (dynamic) sessions or not, their max parallel, otherwise the
    number of static slots;
  - unified KV, which decides which of the other options apply;
  - the KV rolling window;
  - PolyKV and its defaults;
  - the engine's auto fit;
  - KV residency mode;
  - MTP auto;
  - engine selection, defaulting to auto.
- `xollama tweak server gpu` sets:
  - which GPUs may be used;
  - their priority;
  - the backend, where a GPU has more than one;
  - the PCIe link, shown and optionally forced;
  - splitting a model across GPUs, forced on or off, and the split mode.
- `xollama tweak show server|envs|model NAME`: what is set, and what is not.

## 2. Decisions (owner's answers, 2026-10-02)

| Question | Decision |
|---|---|
| Tweak file vs process environment | **The tweak file wins.** Precedence: model setting > server setting > environment > built-in default. `tweak show` names any environment value a setting overrides. |
| When a change applies | **Immediately where possible.** The server re-reads its settings on every change. Load-time settings apply at the next load, and the CLI offers to unload the running models. Settings read once at start say "restart needed" and offer to restart. |
| Command layout | `tweak server` (defaults and the API key), `tweak server gpu`, `tweak envs`, `tweak show server\|envs\|model NAME`. |
| Link speed menu | Probe when the menu opens; show the detected generation and lanes, the measured h2d/d2h, and the figure the engine would use. Force it with a **two-step wizard**: "PCIe generation (detected: 4.0)?", then "PCIe lanes (detected: x8)?". Also accepts a free GB/s figure, or `auto` to clear. |
| Forced link with several GPUs | **Per GPU, by PCI ID.** opencoti is asked for a per-device form (#608). Until it exists, a load gets the slowest forced figure among its devices. |
| GPU priority | **Fill order.** A model that fits on one GPU goes to the highest-priority GPU with room, not upstream's "most free". A split fills GPUs in priority order. |
| Split | Three states: `auto` (upstream: split only when a model does not fit on one GPU), `spread` (always split; today's `OLLAMA_SCHED_SPREAD`), `single` (never split). **Plus** the split mode (`--split-mode`). |
| "Auto" | Primarily the engine's **fit** auto. KV residency mode is supported too, and so is engine selection (default `auto`). opencoti is asked for its full list of auto policies (#608). |

## 3. Design

**Where the settings live.** One file in the *server's* home:
`~/.ollama/xollama-settings.json` (mode 0600, written through `internal/fsowner`).
It has three sections:

- `defaults`: a `types/xollama.Config` minus the per-model-only fields, under
  the same `Validate`;
- `envs`: a map from name to value;
- `gpu`: one entry per device, keyed by PCI ID (allowed, priority, backend,
  forced link), plus `split` and `split_mode`.

The file sits beside `xollama-server.json` (the API key's digest) and is
separate from upstream's `server.json`.

**Who writes it.** The server, through the route `/api/xollama/settings`:

- GET returns the settings, each with its source;
- PUT is accepted from loopback only, and needs the API key while one is set,
  exactly as `/api/xollama/api-key` does.

On Linux the service runs as `ollama` and the CLI usually does not, so the
CLI never writes the file itself. Remote GET works; remote PUT is refused.

**Environment overrides.** `envconfig.Var` and `XollamaOnly` look up the
`envs` overlay before the process environment. This is one surgical hook,
registered in the Registry as `system-settings`. Variables meant for the
engine process — `CUDA_/HIP_/ROCR_/GGML_VK_VISIBLE_DEVICES`, `LLAMA_ARG_*`
and `OPENCOTI_*` — are added to the engine's environment at launch.

Most getters already call `os.Getenv` on every read, so a change applies on
the next read. The rest are read once at start: the listen address, the
models directory, origins and the log level. Those are listed as
"restart needed".

**Server defaults.** They are merged under the model's own layer where the
launch config is built, at `llamaServerConfigForModel`, which feeds `LaunchConfig()`.
A setting the model states wins. `tweak server` reuses the `fields` table of
`tweak model`, filtered to the pertinent fields, so the two cannot drift.

**GPU.**
- Devices come from `/api/xollama/devices`.
- Priority and the split policy become one hook in the scheduler's GPU choice.
- The backend preference and the allowed set use the same mechanism as the
  per-model device pin. A model's own pin still wins.
- The link probe runs on the server (`/api/xollama/link-probe`, which runs
  `opencoti --link-probe` with the device's visible-device env). It is
  refused while a model is loaded on that GPU, until opencoti says otherwise.

**Show.** `tweak show` prints each setting's value and source: model, server,
envs, environment or default. `show envs` also lists the environment values
that the tweak file overrides.

**Off means off.** With no settings file, behaviour is byte-identical to today.
An empty `envs` map adds nothing.

## 4. Phases

- **Phase 0 — specification and engine answers.** Decisions recorded (§2).
  Waiting on opencoti #608:
  - a per-device forced link;
  - the full list of auto policies with their defaults;
  - the matrix of residency mode vs `--kv-rolling-window` (what `window`
    does with the rolling window off);
  - split modes on CUDA and on Vulkan;
  - whether a link probe on a busy GPU is safe.
- **Phase 1 — the store, the route and the env overlay.**
  - The settings file and its type, with validation.
  - `/api/xollama/settings` (loopback PUT).
  - The `envconfig` overlay, and the engine-env pass-through.
  - `tweak envs` and `tweak show envs`.
  - Restart-needed detection and the unload offer.
  - Tests, including "no file means unchanged".
- **Phase 2 — server defaults.**
  - `tweak server` walks the pertinent `tweak model` fields, filtered.
  - The defaults are merged into the launch.
  - `tweak show server`, and `tweak show model NAME` with the source of
    each value.
  - Engine selection, fit auto, MTP auto, PolyKV defaults.
- **Phase 3 — GPU.**
  - `tweak server gpu`: which GPUs are allowed, their priority, the backend
    preference.
  - Split auto, spread or single, plus the split mode.
  - The scheduler hook for fill order.
- **Phase 4 — the link.**
  - The probe route.
  - The generation-then-lanes wizard.
  - A per-device force, or the slowest-figure stopgap.
  - Measured on eleven2go once the owner lifts its pause.
- **Phase 5 — rolling window and residency.**
  - The menu offers only the combinations opencoti confirms.
  - The KV rolling window as a server default, and as a model field.
- **Phase 6 — surfaces and docs.**
  - The desktop app picks up the restart-needed signal.
  - `docs/xollama/tweak.mdx` and the server-settings doc.
  - A feature doc, and the Registry row.

## 5. Execution log

- 2026-10-02: specification settled with the owner; questions to opencoti
  sent as #608.
- 2026-10-02, Phase 1 built:
  - the settings file and the `envconfig` overlay (`Var`, `XollamaOnly`,
    `Environ`, `LookupEnv`);
  - `/api/xollama/settings`, loopback only and never through a proxy;
  - `tweak envs`, and `tweak show server|envs|model`.
  - With no settings file, the environment applies exactly as before.
  - Verified: mutation (the `Var` hook removed fails two tests), the race
    detector, lint, and a live smoke test on a scratch server. The override
    applied at once, the API key and unknown names were refused, the file was
    mode 0600 and was removed once empty, and only `XOLLAMA_HOST` asked for a
    restart.
  - Phase 2 (server defaults) is next. It does not depend on #608 except for
    the auto-policy list.
- 2026-10-02, Phase 2 built:
  - The settings file has a `defaults` section: a `xollama.Config` limited to
    `DefaultSections()` (engine, flash_attention, kv, slots, session, draft).
    DCA, devices and council are refused as defaults.
  - `WithDefaults` merges the defaults under the model's own settings, key by
    key. The model wins. A model that states one half of a cache pair keeps
    its own pair. A section the merge would make invalid for that model is
    left out and logged, and the rest still applies.
  - Applied in `llamaServerConfigForModel` and in the live-slot count.
  - `tweak server` takes every server-pertinent `tweak model` flag (taken
    from the same table), with the same walk and consistency pass. A bare run
    asks whether to change the defaults or the API key.
  - `tweak show server` lists the defaults. `tweak show model` marks each
    setting `model` or `server`, and lists the defaults it did not apply.
  - Verified:
    - mutation: `m.Xollama` in place of `launchXollama(m)` fails the launch test;
    - race tests and lint;
    - a live smoke test: flags set, show, clear, and DCA refused.
  - Still open:
    - the auto policies (fit and the rest) and the KV rolling window, waiting
      on #608;
    - `tweak server gpu` (Phase 3).
- 2026-10-02, opencoti's answers (#609), recorded here:
  1. **Per-device link force: not implemented.** `OPENCOTI_LINK_GBPS` is one
     value per process. opencoti agrees the interim (the slowest forced
     figure among a load's GPUs) is right. A per-device form
     (`PCI=GBPS,...`) is queued at opencoti and needs the owner's go-ahead.
  2. **Auto policies:**
     - `-ngl auto` (the fit);
     - `--fit on|off`, `--fit-target`, `--fit-ctx`;
     - `--vram-target` (0 = all free VRAM minus the compute reserve);
     - `--kv-residency-mode auto|head|window`;
     - `--kv-rolling-window on|off|MiB` (default off; on = R auto);
     - `--pcie-autodetect`;
     - `--sampling-placement device|cpu|auto`;
     - `--auto-mtp-policy measured|allocator|taper|always|off`
       (default measured);
     - `--spec-draft-ngl auto`;
     - the elastic-slot knobs.
     - Legacy, not to offer as serving defaults: the headinfer heads
       fraction, `--neo-pipeline`, `--sparse-attn-topk`.
     - Never to expose: `OPENCOTI_RW_*`, the Vulkan ring test knobs.
  3. **Residency mode × rolling window** (FINAL as a surface):
     - `head` disables the window, and the rolling window is inert.
     - `window` with rolling off is the classic POSITION_WINDOW host tail.
     - `window` with rolling on or a MiB figure is the live slide. It needs
       a single stream (`--kv-unified` or `-np 1`) and CUDA or Vulkan,
       otherwise opencoti warns and falls back to the classic window.
     - `auto` uses the window when the cache overflows, otherwise the cache
       stays resident.
     - Offer: `auto` (the default), and `window` with the rolling window
       on, off or a MiB figure. Offer `head` as "legacy, disables the
       window".
  4. **Split modes:**
     - Only `layer` is validated on every opencoti path.
     - `row` is CUDA only and unvalidated; Vulkan turns it into `layer`.
     - `tensor` is unsupported, so it is not offered.
  5. **`--link-probe` while a model is loaded:** safe when about 64 MiB of
     VRAM and a context are free. The reading drops only while another
     engine is moving data over the link. So allow it when the GPU is idle,
     and refuse it while the engine is generating.
- 2026-10-02, Phase 3 built:
  - `tweak server gpu`: flags per PCI ID, plus an interactive walk with the
    PCIe generation-and-lanes wizard;
  - the `gpu` section of the settings file;
  - scheduler hooks: disabled GPUs out, one backend per GPU, priority first
    in the single-GPU choice and in the device order, split
    auto/spread/single;
  - `llm.DeviceEnvs`: the split mode on loads over several GPUs, and the
    forced link (the slowest of the load's GPUs).
  - Verified: mutation (each of the three placement hooks fails a test),
    race tests, lint, and a live smoke test.
  - The `applyGPUPolicy` call in `processPending` is tested only through its
    function.
- 2026-10-02: the owner approved opencoti's per-device link force. opencoti
  builds it alongside the rolling-window validation and mails when it is
  done. Then `gpuPolicyEnvs` sends every forced device as
  `PCI=GBPS,...` instead of the slowest figure.
- 2026-10-02, Phase 4 built:
  - `/api/xollama/link-probe` runs `opencoti --link-probe` once per GPU
    backend. It is loopback only, and a backend with a model generating on
    it is not probed.
  - `tweak server gpu` probes when the menu opens and shows DETECTED and
    MEASURED H2D columns. The PCIe wizard starts from the detected
    generation and lanes.
  - A failure shows the engine's own reason: the last
    `opencoti --link-probe:` or `fatal error:` line on stderr.
  - Measured live on solidPC with dev build 2610020719001: the RTX 3090 sits
    at PCIe 3.0 x8 in a 4.0 x16 slot, measured 6.7 GB/s host to device and
    6.6 back, and the engine would plan with 6.7. The engine's Vulkan side
    does not list the integrated Renoir.
  - The probe finds the CUDA library through the account's home
    (`~/.llamafile`), as a launch does. A scratch home has none.
- 2026-10-02, Phase 5 built (schema v6):
  - New settings: `kv.rolling_window` (on, off or MiB), `draft.auto_mtp_policy`
    and `fit` (`enabled`, `vram_target_mib`), in
    `types/xollama/engine_policy.go`.
  - They are model settings and, with `fit` added to `DefaultSections`,
    server defaults too.
  - Launch: `appendEnginePolicyArgs` passes `--fit` on either engine. It
    passes `--vram-target`, `--kv-rolling-window` and `--auto-mtp-policy`
    only on opencoti; types/xollama refuses them on a model pinned to
    llamacpp.
  - From #609's matrix, refused: a rolling window on or a MiB figure under
    `head` residency. The tweak walk skips the field under head and says
    why. A server default the model's head residency cannot take is left out
    for that model.
  - The `kv-residency` help now carries #609's semantics, with head as
    legacy.
  - Not offered (#609): the legacy headinfer and neo-pipeline knobs,
    sparse-attn, and the `OPENCOTI_RW_*` test knobs. Sampling placement and
    `--spec-draft-ngl` stay engine defaults for now.
  - Verified: unit tests for validation, merge, launch arguments and tweak
    fields, and the full sweep.
  - Not yet run against a live opencoti, because eleven2go is paused.
- 2026-10-02, Phase 6:
  - feature doc `docs/features/system-settings.md`;
  - user docs in `docs/xollama/tweak.mdx`.
  - The desktop app's own restart is left open. Quitting and reopening the
    app applies a restart-needed setting.
- 2026-10-02, per-device link (opencoti #611, patch 0512, dev build
  2610020826001):
  - `gpuPolicyEnvs` sends `OPENCOTI_LINK_GBPS=<pci>=<GB/s>,...`, one entry
    per GPU of the load with a forced link. A GPU without one is probed.
  - Not gated on the engine build: the owner ruled that there has been no
    first release, and every build is a dev build.
  - Also in #611: the profile now reads the maximum generation × the current
    lane count (#610 item 2), and `--link-probe` lists integrated GPUs
    (item 3).
