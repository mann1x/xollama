---
paths:
  - server/xollama_preload.go
  - server/xollama_keep_alive.go
  - types/xollama/keep_alive.go
  - docs/xollama/preload.mdx
---

# Preloading and a model's own keep-alive (`preload`, `keep-alive` hooks)

- **Two settings, kept apart** (owner, 2026-11-26, issue #74):
  `XOLLAMA_PRELOAD_MODELS` only says what to load at start; how long a model
  stays is the model's own `keep_alive` (`xollama tweak model --keep-alive`).
  The preload never sets a keep-alive of its own: a preloaded model with none
  unloads after `OLLAMA_KEEP_ALIVE`, as any model does.
- **`keep_alive` reads as a request's does** (`ParseKeepAlive`,
  `types/xollama/keep_alive.go`): a duration, seconds, negative = forever,
  `forever` stored as `-1`. `types/xollama` cannot import `api`, so the rule is
  restated; `TestKeepAliveReadsAsARequestDoes` holds it to `api.Duration`.
  It raises **no schema version**: an older build ignores the field and only
  unloads sooner. It is a model's setting, never a server default
  (top-level key outside `DefaultSections`).
- **One scheduler line** (`modelKeepAlive`, `server/xollama_keep_alive.go`,
  after upstream reads the request's in `load()`): request, then model, then
  `OLLAMA_KEEP_ALIVE`. Read at load only, like upstream's. The media twin
  copies `KeepAlive` (`mediaTwin`): the media routes never send one, so
  without the copy a media model ignored it. Guard
  `TestAMediaModelKeptForeverStillUnloadsByName` fails with either removed.
- **Forever is still unloadable**: `xollama stop` / keep_alive 0 reaches the
  runner by name (`expireRunner`, the twin keeps the model's name), `rm`
  unloads first (`delete-unload`), and eviction takes runners in keep-alive
  order, so a forever runner goes last but goes.
- **The preload** (`preloadModels`, one `go` line in `Serve` after the
  VRAM-based default context): one model at a time; text, embedding and a GGUF
  model with media through `scheduleRunner`, a media-only model through
  `mediaRunner` (`preloadPlan`); a cloud model is skipped. The load's context
  is cancelled as soon as it returns, or the runner would stay busy and its
  keep-alive never start. A failure is a Warn; the server starts regardless.
  `XOLLAMA_PRELOAD_MODELS` is in `restartOverrides` (read once).
- Live check (solidPC 3090, side server 22498, 2026-11-26): `/srv/ml/xo53/live.sh`
  and `live-media.sh` — qwen2.5:1.5b, gate-sfx (LLM + sfx) and the media-only
  stable-audio sfx preloaded; forever showed as expires 2319; `xollama stop`
  unloaded each. To pass an env var to a side server, put it on the `env`
  line: `sudo` resets the environment, and does not relay a signal from its
  own process group (stop the server's PID, not sudo's).

```sh
go test ./server/ -run 'KeepAlive|KeptForever|Preload'
go test ./types/xollama/ -run KeepAlive
go test ./cmd/tweak/ -run KeepAlive
```
