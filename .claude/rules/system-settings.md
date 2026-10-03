---
paths:
  - envconfig/xollama_settings.go
  - envconfig/xollama_settings_test.go
  - envconfig/config.go
  - server/xollama_settings.go
  - server/xollama_settings_test.go
  - server/slots_live.go
  - api/xollama_settings.go
  - cmd/tweak/envs.go
  - cmd/tweak/envs_test.go
  - cmd/tweak/show.go
  - cmd/tweak/server.go
  - cmd/tweak/server_test.go
  - types/xollama/defaults.go
  - types/xollama/defaults_test.go
  - types/xollama/engine_policy.go
  - llm/engine_policy_args.go
  - llm/engine_policy_args_test.go
  - llm/llama_server.go
  - discover/opencoti.go
  - plans/system-settings.md
  - docs/xollama/tweak.mdx
---

# Server settings (`system-settings`)

- **The tweak file wins.** The server's `~/.ollama/xollama-settings.json`
  (`envconfig.SettingsFile`) has an `envs` section that overrides the process
  environment. `Var` reads `override(key)` first, the `XOLLAMA_` spelling
  before the `OLLAMA_` one. Guards: `TestTheTweakFileBeatsTheEnvironment`,
  `TestTheXollamaSpellingWinsInsideTheTweakFile`.
- **Off means off.** With no file, or an empty `envs`, every hook is the
  environment as before (`TestNoSettingsFileLeavesTheEnvironmentAlone`). A
  file that cannot be read or parsed overrides nothing and logs a warning; the
  server still starts (`TestABrokenSettingsFileOverridesNothing`).
- **The listen address takes only its `XOLLAMA_` override.** `XollamaOnly`
  calls `overrideOnly`, never `override`: an `OLLAMA_HOST` in the file must not
  steer it (`TestTheListenAddressTakesOnlyTheXollamaOverride`, and
  `.claude/rules/default-port.md`).
- **A process the server starts gets `envconfig.Environ()`, never
  `os.Environ()`**: `SetupLlamaServerCommandEnv` in `llm/llama_server.go` and
  `opencotiListDevices` in `discover/opencoti.go`. An inherited-override check
  uses `envconfig.LookupEnv`, not `os.LookupEnv`. Guard:
  `TestTheEngineSeesTheOverrides`.
- **Only the server writes the file**, through `/api/xollama/settings`
  (`api.XollamaSettingsPath`, `SettingsHandler` in `server/xollama_settings.go`).
  The route is loopback-only and refuses a proxied request, like the API key's
  admin route. `writeSettings` goes through `internal/fsowner`, mode `0600`,
  temp file plus rename, and removes the file when nothing is left. It calls
  `envconfig.ReloadSettings()` after every write.
- **Sections other than `envs` are kept verbatim** (`Settings.Rest`). They
  belong to the packages that read them (`TestOtherSectionsSurviveARoundTrip`),
  through `envconfig.SettingsSection(name)`.
- **The `defaults` section is the server's default for a model's settings.**
  Precedence: the model, then the server's defaults, then the environment,
  then the built-in default. Only `xollama.DefaultSections()` may be defaulted
  (`engine`, `flash_attention`, `kv`, `slots`, `session`, `draft`, `fit`);
  `ValidateDefaults` refuses `dca`, `devices` and `council` (400). `WithDefaults`
  (`types/xollama/defaults.go`) merges key by key under the model's own; a
  section the model cannot act on is skipped and logged, never a refusal, and
  a model stating one half of a KV pair keeps its pair (`cachePairs`). With no
  defaults the model's config is returned as is. Read the merged config through
  `launchXollama(m)` (`llamaServerConfigForModel` in `server/routes.go`,
  `liveSlots` in `server/slots_live.go`), never `m.Xollama`, for a launch
  setting. `SettingsRequest.Defaults` replaces the section whole; an empty
  config clears it. Guards: `TestTheModelWinsKeyByKey`,
  `TestADefaultTheModelCannotUseIsSkippedNotFatal`,
  `TestTheServersDefaultsReachTheLaunchUnderTheModelsOwn`.
- **Engine policies are model and server settings**
  (`types/xollama/engine_policy.go`, schema v6): `fit.enabled`,
  `fit.vram_target_mib`, `kv.rolling_window`, `draft.auto_mtp_policy`.
  `appendEnginePolicyArgs` (`llm/engine_policy_args.go`, one hook line in
  `startLlamaServer` after `appendKVResidencyArgs`) adds `--fit`,
  `--vram-target`, `--kv-rolling-window` and `--auto-mtp-policy`; nothing
  stated adds nothing. `--fit` reaches either engine; the rest are opencoti's,
  refused by `validateEnginePolicies` on a model pinning `llamacpp` and left
  off when stock serves. A rolling window with `kv.residency_mode` `head` is
  refused. Guards: `TestNoStatedPolicyAddsNoArgument`,
  `TestStatedPoliciesReachTheEngine`, `TestTheServerMayDefaultTheEnginePolicies`.
- **`CheckOverride` decides what may be set**: a name in `AsMap()` under either
  spelling, or a `passThroughPrefixes` name the engines read themselves
  (`LLAMA_ARG_`, `OPENCOTI_`, `GGML_`, `CUDA_`, `HIP_`, `ROCR_`, `HSA_`).
  `XOLLAMA_API_KEY` and `OLLAMA_API_KEY` are refused: the key has its own
  command and only its digest is kept on disk.
- **`restartOverrides` lists what is read once at start.** `NeedsRestart`
  reports them and the response names the changed ones in `Restart`. A new
  variable read only at startup gets its row there in the same commit.
- CLI: `xollama tweak envs [NAME=VALUE ...]` (`cmd/tweak/envs.go`),
  `xollama tweak server` for the defaults (`serverFields` in
  `cmd/tweak/server.go`, taken from the `tweak model` field table so the two
  cannot drift; `--clear`, `--dry-run`, `--json`, `--yes`) and
  `xollama tweak show server|envs|model` (`cmd/tweak/show.go`; `show model`
  names each setting's source via `modelSourceRows`), registered in
  `cmd/tweak/tweak.go`; the client call is `Client.Settings` in
  `api/xollama_settings.go`.
- Hooks carry `// xollama-hook: system-settings` in `envconfig/config.go`,
  `llm/llama_server.go` and `server/routes.go`. Registry row `system-settings`
  in `docs/protocols/UPSTREAM-SYNC.md`; plan `plans/system-settings.md`; user
  prose in `docs/xollama/tweak.mdx`.
