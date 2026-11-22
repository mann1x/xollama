---
paths:
  - auth/auth.go
  - auth/xollama_account.go
  - auth/xollama_account_test.go
  - api/xollama_cloud.go
  - server/cloud_accounts.go
  - server/cloud_accounts_test.go
  - server/cloud_pool.go
  - server/cloud_pool_test.go
  - server/cloud_proxy.go
  - server/cloud_send.go
  - server/cloud_status.go
  - server/council_cloud.go
  - server/identity.go
  - server/routes.go
  - types/xollama/cloud.go
  - types/xollama/cloud_test.go
  - cmd/tweak/cloud.go
  - cmd/tweak/cloud_test.go
  - cmd/tweak/server.go
  - docs/xollama/cloud-accounts.mdx
  - plans/cloud-accounts.md
---

# Ollama Cloud accounts (`cloud-accounts` hook)

- **An account is a key.** Each added account is an ed25519 key under
  `~/.ollama/xollama-cloud/<id>/id_ed25519` (`newCloudKey`,
  `server/cloud_accounts.go`, through `internal/fsowner`, 0600), registered
  with its ollama.com account by upstream's connect page (`cloudConnectURL`).
  The primary is upstream's `~/.ollama/id_ed25519`, untouched, and not one of
  the 25 (`xollama.MaxCloudAccounts`).
- **Signing by account goes through the context.** `auth.WithKeyFile(ctx,
  path)`; the one hook line in `auth.Sign` (`accountKeyPath`) signs with it.
  Every ollama.com caller already passes the request's context to `Sign`, so
  never add a second signing path. No key on the context is upstream's
  signing: off means off. Guard `TestARequestSignsWithItsAccountsKey` (fails
  with the hook removed).
- **An account on an API key** (`Auth: "api_key"`, `CloudAuthAPIKey`): the key
  lives in `<account dir>/api_key` (0600, `writeCloudAPIKey`), never in the
  settings or an answer. `auth.WithAPIKeyFile`; the `accountBearer` hook at the
  top of `auth.Sign` returns `Bearer <key>`, which every caller sets as the
  Authorization header (measured: ollama.com takes it on `/api/chat`,
  `/v1/chat/completions` with `?ts=`, `POST /api/me`, `/api/balance`,
  `/api/usage`). `cloudCredPath` / `cloudCredCtx` pick the file and the
  context (dispatch by the file name `api_key*`). A key is checked as
  `api_key.new` before it replaces anything (`checkCloudAPIKey`); a refused
  key is **400**, never 401 — the CLI reads a 401 from an xOllama server as
  "this server wants its own API key" (live check, 2026-11-22). Logout only
  deletes the file: ollama.com has no revoke route. Guards
  `TestAnAPIKeyAccountSendsItsKey`, `TestAPooledAPIKeyAccountSendsItsKey`
  (both fail with the hook removed), `TestAnAPIKeyAccountIsCheckedBeforeItIsKept`,
  `TestAConnectedAccountCanMoveToAnAPIKey`. The CLI reads the key from stdin
  only (`readCloudAPIKey`), never a flag value.
- **Measured (Phase 0, free account with $5 purchased, 2026-11-22)**: plan
  `free`; 5 requests at once → 3 served, 2 queued by ollama.com (no 429); a bad
  key → `401 {"error":"invalid credentials"}`; `/api/balance` is
  `included{balance_usd, allowance_usd, period{from,until}}` +
  `purchased{balance_usd}` with no `session`/`weekly` on that plan. The 402
  exhaustion body is still unmeasured. Live script `/srv/ml/xcloud/live.sh`.
- **The settings hold no email and no key material**: label, plan, a hash
  of the ollama.com id (`cloudAccountHash`) to refuse the same account twice,
  the operator's numbers. Guard `TestAVerifiedAccountKeepsItsPlanButNoEmail`.
  `/api/me` answers are never logged.
- **The same ollama.com account twice is refused** (another added one or the
  primary), and the new key is disconnected again: it would count that
  account's requests twice. Guard `TestTheSameOllamaComAccountIsNotAddedTwice`.
- **Requests at once per account** come from the same plan table as
  Cerebriline's `OLLAMA_PLAN_CONCURRENCY` (free/payg/pro 3, max/team 10;
  `xollama.PlanParallel`); keep the two in step. Precedence: the account's
  `parallel`, then `parallel_per_account`, then the plan.
- **The admin route is loopback only and refuses proxy headers**
  (`CloudAccountsHandler`, `/api/xollama/cloud/accounts`), as the settings and
  API-key routes are. Writes go through `updateCloud` under `settingsWrite`,
  validated, and `writeSettings` removes the file when nothing is left.
- ollama.com is reached through `cloudWhoami` / `cloudDisconnect`; tests
  replace them (`withFakeOllamaCom`). Never call the real ollama.com in a test.
- **The pool** (`server/cloud_pool.go`): slots per account; the first
  `accounts_at_once` ready accounts (primary first) are active; a request takes
  the active account with the most free slots, else waits in a strict FIFO
  (`grantLocked` hands slots to waiters in order; never let a newcomer barge
  past a waiter). No cloud section, no pool: `acquire` returns nil, nil and the
  caller takes upstream's path (`TestWithoutACloudSectionThereIsNoPool`).
- **Outcomes** (`classifyCloud`): 402 or a usage-limit text = exhausted (out
  until the balance's reset, `cloudBalanceResets`, else `cloudExhaustedFor`
  15 min); 401 = left out `cloudSignedOutFor` (10 min); 429 = busy, never
  exhausted (`TestA429NeverMarksAnAccountExhausted`). A request is sent again
  on another account only before anything reached the client: the passthrough
  reads the status before copying, the stub path checks `c.Writer.Written()`.
- **Two hooks send through the pool and nothing else does**: `sendCloudRequest`
  in `proxyCloudRequestWithPath` (only `cloudPoolPaths`, the inference routes)
  and `cloudStubCall` around the stub's `client.Generate` / `client.Chat`.
  `/api/me`, usage, balance, show and web search stay on the primary.
- **Goroutines get their swappable functions passed in** (`learnReset(m,
  cloudBalanceResets)`): reading the package var from the goroutine raced with
  a test's cleanup under `-race`.
- **`accounts` is every configured account, never only the usable ones**
  (`status`): signed out, pending and disabled are listed with their state and
  count 0 in `parallel`/`parallel_now`. With only the usable ones counted, a
  logout read as `accounts 0` = "no pool", and Cerebriline fell back to the
  `/api/me` cap of the (excluded, unsigned) primary (live check, 2026-11-22).
  Guard `TestAConfiguredPoolNeverReadsAsNoPool`.
- **The primary's sign-in is the pool's to learn** (`askPrimaryLocked`, run
  from every `refreshLocked`): the first answer waited for (`cloudPrimaryFirstAsk`
  5 s, once per process, so a client's first reading is right), then in the
  background hourly, every `cloudPrimaryOutRecheck` (2 min) while signed out.
  A primary known signed out is listed `signed_out` and is no member. Before
  this, the ask ran only when the settings changed. Guard
  `TestAPrimaryThatIsNotSignedInTakesNoRequests`.
- **What a client reads** (`server/cloud_status.go`): `/api/xollama/cloud`
  (`CloudPoolStatus`: counts, labels, plans, states; no email, id, home path or
  key; `TestTheStatusRouteNamesNoPerson`) and `/api/xollama/cloud/balance`
  (each account's `/api/balance`, at most once a minute per account). Feature
  `cloud_accounts_v1`. `council.cloud_parallel` defaults to `capacity()` (at
  most 16), read at each council turn's start (`councilCloud.slots` remakes
  the count when its size changes), so a changed pool reaches a council at its
  next turn.
- Plan `plans/cloud-accounts.md`; docs `docs/xollama/cloud-accounts.mdx`;
  Registry row `cloud-accounts`.

```sh
go test ./auth/ ./types/xollama/ ./cmd/tweak/ -run 'Account|Cloud|AddPrints|PoolAndSet'
go test ./server/ -run 'Cloud|Account|OllamaCom|Pool|Exhausted|RunOut|429|OwnKey|PulledCloud|NamesNoPerson|Balance'
```
