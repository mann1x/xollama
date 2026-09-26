# The listen address

- xollama listens on **22434**, from `envconfig.DefaultPort`. Never write the
  literal again: `Host()` and `server/create.go`'s `remoteURL` both read the
  constant, and the tests reference it too.
- **`XOLLAMA_HOST` only.** `Host()` calls `XollamaOnly("OLLAMA_HOST")`, not
  `Var()`. `OLLAMA_HOST` is what a stock ollama install exports, often
  machine-wide — reading it would put both servers back on one socket and make
  the separate port worthless. This is the **only** exclusively-namespaced
  setting; every other `OLLAMA_*` var keeps its fallback, because a cache type
  is shareable and an address is not.
- **Binding and connecting are different questions.** `serve` binds
  `envconfig.Host()` and nothing else. Only the CLI's *connect* side, when no
  host is named, may fall back to 11434: `ResolveHost` in `api/xollama_host.go`,
  run once from `checkServerHeartbeat` via `cmd/xollama_host.go` (never from
  `api.ClientFromEnvironment`, which must stay a pure function of the env). It
  tells the fork apart by `/api/xollama` (`api/xollama_identity.go`,
  `server/identity.go`), then by the fork's name in `/api/version` — required,
  because every xollama built before the route 404s it exactly as a stock
  ollama does. `api.IsXollama` is that same probe, exported for the council's
  remote members (`server/council_remote.go`); do not write a second one.
- **A stock ollama on the fallback port is a refusal naming `XOLLAMA_HOST`,
  never a silent connection.** `pull`, `rm` and `tweak model` write, and would
  write into *that* server's store.
- **Side-by-side is not supported.** The port and the variable remove an
  accidental collision, nothing more. One model store with two writers, cache
  entries owned by two users, one GPU with two schedulers, one
  `<install>/lib/ollama` — all still there. `docs/xollama/default-port.mdx`
  says so in a Warning; do not soften it into "supported" when describing the
  feature elsewhere.
- **Two tests hold the two halves**, and each fails alone:
  `TestTheDefaultPortIsNotUpstreams` pins the number (the others reference the
  constant and would follow it anywhere), and
  `TestTheListenAddressIsNotInheritedFromAStockOllama` is the only test in the
  tree that fails if `Host()` goes back to `Var()`. Verified by removal.
- **A test that sets `OLLAMA_HOST` no longer steers anything.** Use
  `XOLLAMA_HOST`. An expectation of the default only moves to 22434 when its
  test sets no host at all — several launcher tests set one explicitly and must
  keep asserting against it. A blanket search-and-replace gets this wrong; it
  also hit an unrelated `11434` used as an embedding length in `cmd/cmd_test.go`
  and a `22434` already used as an arbitrary "other host" in
  `cmd/launch/codex_app_test.go` (since moved to 19999).
- Registry rows `default-port` and `host-namespace` in
  `docs/protocols/UPSTREAM-SYNC.md`.
