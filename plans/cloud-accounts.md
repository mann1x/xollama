# Several Ollama Cloud accounts on one server

**Status:** ACTIVE. Phases 1–3 built 2026-11-22: keys, the `cloud` section, signing by account, the admin route, `xollama tweak server cloud` (1); the pool, both send paths, the council default (2); `/api/xollama/cloud`, `/api/xollama/cloud/balance`, `cloud_accounts_v1` (3). API-key accounts added (§3.8). Phase 0 measured and the live check passed on the owner's dummy account, except the 402 exhaustion body. Phase 4 done: Cerebriline caps per server (cline cb47c75291, messages a1f98a0455) and passed all five live checks against a pooled dev server on 2026-11-22. Open: the 402 exhaustion body (unmeasured) and the hand-over between two real accounts (one funded account only); both are covered by the fake-ollama.com tests. Live script: `scripts/gates/cloud-accounts.sh`. Owner's decisions in §6; build order 1–3, then 0 and 5. **Owner:** xollama. Cerebriline's side is
the `ollama` session's (`ollama@solidpc`), by mail.

## 1. What the owner asked for (2026-11-22, condensed)

- Cloud models served through more than one ollama.com account: up to **25
  additional accounts**, configured and saved in the server's configuration.
- Sign in, sign out, add, change and remove them with `xollama tweak`.
- **When one account runs out, the next one is used.** Order does not matter.
- A setting for **how many accounts are used at once**. With Pro accounts (3
  requests at once each), 3 accounts at once gives 9 parallel requests when
  the clients ask for that many.
- Aligned with Cerebriline, which already caps its requests per ollama.com
  account. The xollama provider must be able to read the accounts setup and
  adjust its cap by itself.

## 2. How it works today (read 2026-11-22)

**xollama (upstream code):**
- An ollama.com account is the **ed25519 key** in `~/.ollama/id_ed25519`.
  `signin` registers its public key with the browser's ollama.com session
  (`https://ollama.com/connect?name=<host>&key=<pubkey>`, `signinURL` in
  `server/routes.go`). `signout` removes it (`client.Disconnect`, DELETE
  `/api/user/keys/<key>`).
- Every request to ollama.com is signed by `auth.Sign(ctx, challenge)`
  (`auth/auth.go`), which reads that one key file each time. Its callers:
  - the cloud passthrough, `signCloudProxyRequest` (`server/cloud_proxy.go`),
    for model names ending in `:cloud` on `/api/chat`, `/api/generate`, the
    OpenAI and Anthropic routes;
  - the `api.Client` (`api/client.go:96`), which serves a pulled cloud stub (a
    model with `RemoteHost`, in `GenerateHandler` and `ChatHandler`);
  - `/api/me`, `/api/balance` and `/api/usage` (`WhoamiHandler`,
    `BalanceHandler`, `UsageHandler`);
  - registry pushes and pulls (`server/auth.go`), web search
    (`anthropic/anthropic.go`), and the desktop app's tools and updater.
- **xollama places no cap of its own on cloud requests.** The one exception is
  a council's cloud members: `council.cloud_parallel`, 3 by default
  (`server/council_cloud.go`).

**Cerebriline** (`/srv/dev-disk-by-label-opt/dev/cline`, commit 6caa7785d9,
release notes 4.100.292):
- `sdk/packages/llms/src/providers/vendors/ollama-account-slots.ts`: one FIFO
  semaphore per ollama.com account, wrapped around the chat POSTs of the
  `ollama` and `xollama` providers, cloud models only. The account is told
  apart by a hash of the `/api/me` id.
- The limit comes from the **plan string alone**, in the table
  `OLLAMA_PLAN_CONCURRENCY`: free/pay-as-you-go 3, pro 3, max 10, team 10
  (source comment: ollama.com/pricing, 2026-11). A plan missing from the table
  gets no cap.
- `agent-slots.ts` (`resolveAgentSlotLimit`) caps delegated agents at
  `min(configured, plan limit)`.
- `/api/me` is read **once per process**; nothing refreshes it.
- What ollama.com says when an account runs out: HTTP **402**, "you have
  reached your session usage limit". Cerebriline only flags it
  (`escalation-session.ts:270`). A 429 is retried at most 4 times, honouring
  `Retry-After` (`retry-rate-limit.ts`).
- Usage: `GET /api/balance` and `GET /api/usage?range=24h|7d|30d` through the
  local server, which signs them. The fields read are
  `included.balance_usd`, `included.session` / `included.weekly`
  `{remaining_percent, resets_at}`, and `purchased.balance_usd`.
  `/api/balance` allows 10 requests a minute per user.
- **Against a multi-account xollama today**, Cerebriline would read the
  primary account's plan from `/api/me` and stop at 3, whatever the pool holds.

## 3. Design

### 3.1 Accounts are keys

- Each additional account is **its own ed25519 key**, under
  `~/.ollama/xollama-cloud/<id>/id_ed25519`. The key is created by the server,
  as the server's account (`internal/fsowner`, mode 0600), and never leaves
  the host.
- Signing in is upstream's flow with that key. The connect URL is opened in a
  browser **signed in to the ollama.com account being added**. Adding several
  accounts means switching the browser's ollama.com account (or using a
  private window) between them.
- An account is verified by `/api/me` signed with its own key. That gives its
  id, name and plan.
  - An account whose `/api/me` id is already configured is **refused**: two
    keys on one account would count its 3 requests twice.
- The primary account (`~/.ollama/id_ed25519`, set by `xollama signin`) stays
  upstream's. It is **in the pool as account 0** unless excluded
  (`cloud.primary: exclude`), and it does not count toward the 25.
- Registry pushes and pulls, web search, the desktop app and `/api/me` keep
  signing with the primary key. Only inference is spread over the pool.

### 3.2 Signing by account (`cloud-accounts` hook)

- One marked hook in `auth.Sign`: when the context carries an account
  (`auth.WithKey(ctx, key)`, additive file `auth/xollama_account.go`), it
  signs with that key; otherwise it reads the default file as today.
- Both inference paths already pass the request's context down to the
  signer:
  - the passthrough uses `outReq.Context()`;
  - the stub path calls `client.Chat(c.Request.Context(), …)`.
  So the pool chooses an account, puts it on the context, and nothing else in
  the signing path changes.
- **Off means off.** With no `cloud` section, no context carries a key, there
  is no semaphore, and every request is upstream's byte for byte.

### 3.3 Settings: the `cloud` section

The section lives in `xollama-settings.json`, decoded strictly, and is written
only through the admin routes:

```json
"cloud": {
  "accounts": [
    {"id": "a1b2c3", "label": "work-2", "plan": "pro", "parallel": 0,
     "disabled": false, "verified_at": "2026-11-22T19:00:00Z"}
  ],
  "accounts_at_once": 3,
  "parallel_per_account": 0,
  "primary": "include"
}
```

- `parallel`, per account, and `parallel_per_account`: 0 means "from the
  plan", using the same table as Cerebriline (free/payg/pro 3, max/team 10).
  A plan missing from the table gets 3, said at Warn.
- `accounts_at_once` (K): how many accounts take requests at the same time.
  0 means all of them.
- The section holds **no email and no key material**. Only the label, the
  plan and the id hash.
- At most 25 additional accounts. `Validate` refuses a 26th, a duplicate
  label, a duplicate id, and a negative number.

### 3.4 The pool: which account serves a request

- **Slots.** Each account has `parallel` slots. The **active set** is K
  accounts that are signed in, enabled and not exhausted. A cloud request
  takes a free slot on the active account with the most free slots.
  - With every slot busy, the request **waits in a FIFO queue**, and leaves
    the queue when its client cancels.
  - Total capacity is the sum of the active accounts' slots: 3 Pro accounts at
    once = 9.
- **Exhausted.** An account is marked exhausted by:
  - a **402** from ollama.com;
  - a body saying "usage limit";
  - its balance poll showing `included.session` or `included.weekly` at 0%,
    with no purchased balance left.
  It leaves the active set until its `resets_at` (from its balance; with none,
  a probe every 15 minutes). The next ready account takes its place. Order
  does not matter.
- **Retried on the next account.** A request that gets a 402 or usage-limit
  answer **before any byte has reached the client** is sent again on the next
  account. On the passthrough the status is known before the body is copied.
  On the stub path `Client.Chat` returns the status error before calling the
  response callback.
  - A failure after streaming began goes to the client as it is.
- **A 429 is not exhaustion.** It can come from the same account being used
  elsewhere at the same time. The request is retried on another account with
  a free slot. If there is none, the 429 goes to the client with ollama.com's
  `Retry-After`.
- **Balance poller.** One `/api/balance` per account per minute at most, and
  only while the pool is in use. This stays inside the 10-per-minute per-user
  limit.
- **All accounts exhausted.** The client gets ollama.com's 402, with a message
  naming the earliest reset.
- **Council.** `council.cloud_parallel` defaults to the pool's capacity when
  a pool is configured. A stated value still wins.

### 3.5 `xollama tweak server cloud`

`xollama tweak server cloud` follows `tweak server gpu`: an interactive menu
with no arguments, and these subcommands:

| Command | Does |
|---|---|
| `list` | each account: label, plan, slots, state (active / ready / exhausted until … / signed out / disabled), and K and the total capacity |
| `add [--label L]` | creates the key, prints the connect URL, and waits up to 10 min for `/api/me` to answer with the new key; stores the account once it is verified |
| `login <label>` | a new connect URL for an existing account whose key ollama.com no longer accepts |
| `logout <label>` | removes the key at ollama.com and keeps the entry, signed out |
| `set <label> [--label L] [--parallel N] [--disable/--enable]` | change one account |
| `remove <label>` | logout, then delete the key and the entry |
| `--accounts-at-once N`, `--parallel-per-account N`, `--primary include/exclude` | pool settings |

- Admin routes: `/api/xollama/cloud/accounts` (GET, POST, PATCH, DELETE) and
  `…/accounts/<id>/signin`, **loopback only**, refusing proxy headers, as
  `/api/xollama/api-key` does.
- `xollama tweak show server` prints the section.

### 3.6 What a client can read: `GET /api/xollama/cloud`

- Feature `cloud_accounts_v1` in `/api/xollama`. The route is readable by any
  client that passes the local API key.
- What it returns:

```json
{"accounts": 4, "accounts_at_once": 3, "parallel": 9, "parallel_now": 9,
 "in_use": 2, "queued": 0, "exhausted": 1, "next_reset": "2026-11-23T00:00:00Z",
 "pool": [{"label": "work-2", "plan": "pro", "parallel": 3, "state": "active",
           "resets_at": null}]}
```

- `parallel` is what the configuration gives. `parallel_now` is what is ready
  right now (exhausted accounts left out). No email, id or key.
- With no pool configured, the route answers `{"accounts": 0}`, so a client
  keeps its own per-account cap.
- `/api/me`, `/api/balance` and `/api/usage` stay upstream's (the primary
  account). Each account's balance is `GET /api/xollama/cloud/balance`,
  served from the poller's cache, so a client polling it never spends the
  accounts' 10-per-minute limit.

### 3.7 Cerebriline (for the `ollama` session, by mail)

- In the `xollama` provider, when `/api/xollama` lists `cloud_accounts_v1`
  and `/api/xollama/cloud` reports `accounts > 0`:
  - the slot key is the **server** (`xollama:<origin>`), not the account hash;
  - the limit is `parallel_now`, read every 60 s and again after a 402 or 429.
    Today's semaphore already drains when its limit rises;
  - `resolveAgentSlotLimit` takes the same number.
- `parallel_now` 0 (everything exhausted) means: say the next reset to the
  user, and do not hammer the server.
- No change for the `ollama` provider, nor for an xollama without the feature.

## 4. Phases

0. **Facts on real accounts** (needs two ollama.com accounts on one host):
   - two keys signed in at once from one host;
   - per-key `/api/me` and `/api/balance`;
   - the exact 402 body and headers;
   - what a fourth simultaneous request on a Pro account gets (429? queued by
     ollama.com?).
   Record them in this plan before Phase 2's rules are written.
1. **Keys and settings:** `auth/xollama_account.go` plus the `auth.Sign` hook;
   the `cloud` section (`types/xollama/cloud.go`, `Validate`); the admin
   routes; `tweak server cloud` add/login/logout/set/remove/list; docs page
   `docs/xollama/cloud-accounts.mdx`.
2. **The pool:** `server/cloud_pool.go` (slots, active set, queue,
   exhaustion, retry, balance poller), wired into `proxyCloudRequestWithPath`
   and the two stub paths with marked hooks; the council default.
3. **Client view:** `/api/xollama/cloud`, `/api/xollama/cloud/balance`, the
   feature flag; the rules file `.claude/rules/cloud-accounts.md`; Registry
   rows.
4. **Cerebriline:** mail the `ollama` session the route and its semantics,
   then check their change against a live pool.
5. **Gate:** two accounts or more. Measure:
   - the total parallel requests reached;
   - a forced exhaustion handing over to the next account mid-load, with no
     client error;
   - Cerebriline's cap following `parallel_now`.
   Row in `docs/protocols/VALIDATION.md` when it ships.

## 3.8 Accounts on an API key (owner, 2026-11-22)

An account can be an ollama.com API key instead of a connected ed25519 key.
`auth.Sign` answers `Bearer <key>` for a context naming the key's file (one
more line in the `cloud-accounts` hook), so the passthrough and the stub client
send it with no other change. The key is checked with `POST /api/me` before it
is kept, stored in the account's directory, never in the settings; the CLI
reads it from stdin. Logout forgets it (ollama.com has no revoke route).

## Phase 0, measured (2026-11-22, the owner's dummy account: free, $5 purchased)

| Question | Answer |
|---|---|
| API key auth | `Authorization: Bearer <key>` works on `POST /api/me`, `/api/balance`, `/api/usage?range=24h\|7d\|30d`, `/api/chat`, `/v1/chat/completions`, with `?ts=` |
| Bad key | `401 {"error":"invalid credentials"}` |
| 4th+ request at once (free = 3) | queued by ollama.com, not refused: 5 at once → 3 at ~2.4 s, 2 at ~4.5 s |
| `/api/me` | `{"ID":…,"Plan":"free",…}` (GET is 405) |
| `/api/balance` (free + purchased) | `included{balance_usd 0, allowance_usd 0, period{from,until}}`, `purchased{balance_usd}`; no `session`/`weekly` |
| 402 body at exhaustion | not measured (would spend the $5) |

Live check of the built pool with that account (`scripts/gates/cloud-accounts.sh`):
add/login by API key, chat on both routes through it (primary excluded and
keyless), 5 at once → `in_use 3, queued 2`, balance route, logout → 402 from
the pool, remove. All as designed.

## 4a. As built (2026-11-22)

- One deviation from §3.4: the balance is not polled every minute while the
  pool is in use. It is read once, when an account runs out, to learn its
  reset (`learnReset`), and on demand by `/api/xollama/cloud/balance` (cached
  a minute per account). Polling to find an account at 0% before a request
  fails is left for Phase 0, once a real balance body is in hand.
- The council's `cloud_parallel` default follows the pool's capacity. Each
  council turn reads it when it starts (`councilCloud.slots`, remade when the
  size changes), so a changed pool reaches a council at its next turn; a turn
  already running finishes on the count it started with, and the pool's own
  queue still bounds every account.

## 5. Guards

- No `cloud` section: no semaphore, and requests signed with the default key
  (upstream byte for byte).
- An account's request is signed with its own key, never another's.
- A 402 before the first byte is retried on the next account. A 402 after
  streaming began reaches the client as it is.
- A 429 never marks an account exhausted.
- An exhausted account comes back at its `resets_at`.
- K accounts at once × slots per account is the capacity, and the queue holds
  the rest.
- A duplicate account (same `/api/me` id) is refused.
- The 26th account is refused.
- The settings file never holds an email or a key.
- `/api/xollama/cloud` carries no email, id or key.

## 6. Decisions (owner, 2026-11-22)

| Question | Decision |
|---|---|
| Primary account in the pool | **Yes, as account 0**, not counted in the 25, and it can be taken out of cloud-model use (`--primary exclude`). It keeps registry pushes and pulls, web search and `/api/me` either way. |
| Every slot busy | **Wait in a queue**, in order, leaving it when the client cancels. |
| Real accounts for Phase 0 | **Later.** Phases 1–3 are built and tested against a fake ollama.com (signed-request checks, scripted 402 and 429 answers). Phase 0 and the live gate run when accounts are available, and any rule a Phase 0 fact contradicts is changed then. |
