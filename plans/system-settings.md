# Server settings without environment variables

**Status:** ACTIVE. Phase 0 is waiting on opencoti's answers to #608; Phase 1
was built 2026-10-02. **Owner:** xollama.

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
