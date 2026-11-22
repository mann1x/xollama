---
paths:
  - auth/auth.go
  - auth/xollama_account.go
  - auth/xollama_account_test.go
  - api/xollama_cloud.go
  - server/cloud_accounts.go
  - server/cloud_accounts_test.go
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
- Plan `plans/cloud-accounts.md`; docs `docs/xollama/cloud-accounts.mdx`;
  Registry row `cloud-accounts`.

```sh
go test ./auth/ ./types/xollama/ ./cmd/tweak/ -run 'Account|Cloud|AddPrints|PoolAndSet'
go test ./server/ -run 'Cloud|Account|OllamaCom'
```
