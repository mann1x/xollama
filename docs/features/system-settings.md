# Server settings without environment variables

Plan: [plans/system-settings.md](../../plans/system-settings.md). Registry
row: `system-settings` in [UPSTREAM-SYNC.md](../protocols/UPSTREAM-SYNC.md).
User docs: [docs/xollama/tweak.mdx](../xollama/tweak.mdx).

## What it is

Upstream configures a server through environment variables. xollama keeps the
same settings as real configuration, in one file in the server's home:
`~/.ollama/xollama-settings.json`, mode 0600. Only the server writes it,
through `/api/xollama/settings`, which answers only a client on the server's
own machine (never one coming through a proxy), and needs the key while one is
set.

| Section | Set with | Read by |
|---|---|---|
| `envs` | `xollama tweak envs` | `envconfig.Var` / `XollamaOnly` first, before the environment; `envconfig.Environ()` for every engine process |
| `defaults` | `xollama tweak server` | `WithDefaults` under the model's own settings, at launch (`launchXollama`) |
| `gpu` | `xollama tweak server gpu` | `applyGPUPolicy`, `schedSpread`, `neverSplit`, `priorityOrder` in the scheduler; `llm.DeviceEnvs` for the split mode and the forced link |

`xollama tweak show server|envs|model NAME` reads it back with the source of
every value.

## Precedence

A model's own setting comes first, then the server's default, then the
environment (the `envs` section beats the process environment), then the
built-in default. The owner ruled that the tweak file wins over the
environment (2026-10-02).

## Rules that hold it together

- **Off means off.** With no file, every hook returns what it was given.
  `TestNoSettingsFileLeavesTheEnvironmentAlone`,
  `TestNoDefaultsLeaveTheModelAsItIs` and `TestNoGPUPolicyChangesNothing`
  check this.
- **The API key is not an env override.** `XOLLAMA_API_KEY` is refused there:
  the key keeps only its digest on disk.
- **A server default never refuses a model.** If merging a section would make
  the model's config invalid, that section is left out for that model and
  logged. A cache pair (`kv.k`/`kv.v`) is never split between the model and
  the server.
- **A model's own device pin wins over the GPU policy**, a disabled GPU
  included.
- **A forced link is per GPU**: `OPENCOTI_LINK_GBPS=<pci>=<GB/s>,...`
  (opencoti patch 0512) lists each GPU of the load with a forced speed, and a
  GPU without one is probed. The engine plans each KV cache with the slowest
  of the GPUs holding its layers.
- **The link probe** is not run on a backend with a model generating on it,
  because the reading would come out low. An idle loaded model does not
  matter (opencoti #609).
- **Engine policies offered**: only opencoti's validated combinations. The
  rolling window under `head` residency is refused. The split modes offered
  are `layer` and `row`, without `tensor`.

## What applies when

Most variables are read again where they are used, and settings that act at
load time apply at a model's next load: the command offers to unload the
running models. The listen address, the models directory, origins, the log
level, GPU visibility and the proxies are read once at start
(`envconfig.NeedsRestart`). For those the command says a restart is needed.
The desktop app does not restart its server on its own yet. Quitting and
reopening it applies them.
