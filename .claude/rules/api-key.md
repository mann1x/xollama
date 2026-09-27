---
paths:
  - api/client.go
  - api/xollama_apikey*.go
  - api/xollama_host.go
  - api/xollama_identity.go
  - envconfig/xollama_apikey*.go
  - server/xollama_apikey*.go
  - server/routes.go
  - cmd/tweak/server*.go
  - cmd/xollama_host.go
  - app/ui/apikey.go
  - docs/xollama/api-key.mdx
---

# The local API key

- **Local only.** It guards connections *to* this server. Registry pushes and
  pulls, and cloud models, keep ollama.com signing. `XollamaOnly("OLLAMA_API_KEY")`
  gives `XOLLAMA_API_KEY`: never read `OLLAMA_API_KEY` for it, because that is an
  ollama.com key (the qwen launcher uses it).
- **Off means off.** With no key configured the middleware calls `Next` and the
  client adds no header.
- **Only `ClientFromEnvironment` carries the key** (`keyedClient`). `NewClient`
  for another host must never get it, and that includes council remote members.
  Inside the server the per-process token comes first, so a stale key file in
  the service account's home cannot break self-calls.
- **Throttle on `c.RemoteIP()`, never `ClientIP()`.** gin trusts
  `X-Forwarded-For` from anyone. Count only a *wrong* key: counting missing keys
  throttled every route after 10 unkeyed probes (caught by a test).
- **Strip `Authorization` and `x-api-key` after the check.** The cloud
  passthrough copies incoming headers upstream.
- **The admin route** (`/api/xollama/api-key`) is loopback-only and refuses any
  request with a proxy header. Behind a TLS proxy on this host every client
  looks like loopback. Validate the action before the env-source 409.
- **A local-key 401 must stay a `StatusError`** (`localKeyError`, first in
  `checkError`). As an `AuthorizationError` the CLI offers an ollama.com
  sign-in.
- **The probe never sends the key.** It recognises `realm="xollama"` on a 401.
  The candidate may be a stock ollama on 11434.
- **The client key file is the caller's own secret:** write it with `os`, not
  `internal/fsowner`. Under root, fsowner's fallback would hand it to the
  `ollama` account. The *server's* digest file does use fsowner.
- **The key is never a command-line value.** `xollama tweak server --api-key`
  (`cmd/tweak/server.go`) takes `generate` / `set` (stdin) / `remove` /
  `status`, so the key stays out of shell history.
- Docs: `docs/xollama/api-key.mdx`, whose TLS Warning stays prominent. Registry
  row `api-key`.
