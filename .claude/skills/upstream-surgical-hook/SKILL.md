---
name: upstream-surgical-hook
description: Applies a change to a pre-existing file in the upstream ollama tree (server/, llm/, api/, cmd/, envconfig/, model/, thinking/, openai/) the only sanctioned way: triage additive-file vs carried-patch vs surgical hook, add the `// xollama-hook:` marker plus its row in the Registry of docs/protocols/UPSTREAM-SYNC.md in the same commit, gate the change so XOLLAMA_ENGINE=llamacpp stays byte-identical to upstream v0.34.2, and verify with build/vet/test plus the marker-vs-Registry reconciliation. Use when the user says 'change ollama's server', 'patch an upstream file', 'add a hook', 'modify routes.go/llama_server.go/server.go', 'gate this behind XOLLAMA_*', or when any edit lands on a file that already existed at tag v0.34.2. Do NOT use for brand-new xollama-only files (those are additive and need no hook), for docs/ or .wolf/ edits, for the app/ui TypeScript tree, or for performing a full upstream merge (that is the sync workflow in UPSTREAM-SYNC.md).
paths:
  - server/**/*.go
  - llm/**/*.go
  - api/**/*.go
  - cmd/**/*.go
  - envconfig/**/*.go
  - model/**/*.go
  - thinking/**/*.go
  - openai/**/*.go
  - anthropic/**/*.go
  - middleware/**/*.go
  - docs/protocols/UPSTREAM-SYNC.md
  - docs/protocols/CARRIED-PATCHES.md
---
# Upstream surgical hook

Every xollama change is **either an additive file or a marked surgical hook**. This skill covers the hook path and the triage that must happen before you take it.

## Critical

Run these before writing a single line:

1. **Is the file upstream's?**

   ```sh
   git cat-file -e v0.34.2:<path> && echo UPSTREAM || echo ADDITIVE
   ```

   The v0.34.2 tag (commit dfabde45) is the seed commit of `main`. If it prints `ADDITIVE`, stop — write the file normally, no marker, no Registry row. A brand-new file inside an upstream directory (a new engine-resolver package under `llm/`, or a new xollama file alongside `envconfig/config.go`) is still additive: conflicts come from touching upstream *lines*, not from sharing a folder.
2. **Never rename the Go module path.** It stays upstream's, in `go.mod` and in every import. Measured cost of renaming: 155 of the 474 files upstream touched between the v0.34.0 and v0.34.2 tags — a ~33% conflict rate per sync, forever.
3. **Off means off.** With `XOLLAMA_ENGINE=llamacpp` and no xollama flags set, behaviour must be byte-identical to upstream. Every hook is gated; an unconditional behaviour change is not a hook, it is a fork divergence.
4. **Wrap, never rename.** Do not rename an upstream symbol, do not reformat, do not refactor "while we're here". Minimum-line edit only: one import plus one call site, or one conditional.
5. **Marker and Registry row go in the same commit.** A marker with no Registry row is a sync failure.
6. Files on the **read-line-by-line** list — `model/parsers/`, `thinking/`, `llm/llama_server.go`, `llm/server.go`, `server/routes.go` — are the same surface the carried patches touch and the surface upstream edits most. Extra care there: read the existing carried-patch code in the region before editing it.

## Instructions

### Step 1 — Triage the change into one of three buckets

| Bucket | Test | Where it goes |
|---|---|---|
| **Additive** (preferred) | the effect can be had from a new file | a new package under `llm/`, `docs/features/`, `docs/protocols/`, or a new xollama file in the owning package |
| **Carried patch** | the change is a bug fix or feature that *upstream should take* | branch on the fork remote (mann1x/ollama), own `--no-ff` merge, row in `docs/protocols/CARRIED-PATCHES.md` — **not** a hook, no marker |
| **Surgical hook** | xollama-only behaviour that cannot be reached additively | marked edit in the upstream file + Registry row |

Ask: *would ollama merge this?* If yes it is a carried patch — stage it against the fork remote, open the upstream PR, and record it in `docs/protocols/CARRIED-PATCHES.md` (see the existing 12 rows for the table shape). Carried patches look like upstream code and carry **no** `xollama-hook:` marker.

**Verify before Step 2:** state out loud which bucket the change is in and why the other two do not apply. If the bucket is Additive or Carried patch, leave this skill here.

### Step 2 — Write (or locate) the plan doc

Every hook points at a plan. Existing plans: `docs/features/engine-opencoti-llamafile.md`, `docs/features/rebrand.md`. If none covers the change, create a new plan file under `docs/features/` first, with what the feature does, how it is gated, and what happens when it is off.

Pick the feature id now — lowercase kebab-case, matching the plan filename stem (`engine-opencoti-llamafile`, `rebrand`). This id is used verbatim in Step 4 and Step 5.

**Verify before Step 3:**

```sh
ls docs/features/<feature-id>.md
```

### Step 3 — Put the logic in an additive file, not in the hook

The hook is a call site, not an implementation. Write the behaviour as an exported function in an additive file first — a new `llm/engine` package (`package engine`), or unexported helpers alongside, in the shape of `llm/repeat_guard.go`. Tests go beside it in a sibling test file, using `testify` or plain `t.Errorf`, matching `llm/repeat_guard_test.go`.

Gate reads go through `envconfig`. `envconfig.Var` in `envconfig/config.go` is the single chokepoint for env reads:

```go
// XollamaEngine reports the selected engine: "llamacpp" (upstream, default)
// or "opencoti".
func XollamaEngine() string {
	if v := Var("XOLLAMA_ENGINE"); v != "" {
		return v
	}
	return "llamacpp"
}
```

**Verify before Step 4:**

```sh
go test ./llm/...
```

passes with the new helper and its test, before any upstream file is touched.

### Step 4 — Make the minimum edit and mark it

This step uses the feature id from Step 2 and the helper from Step 3. The marker comment goes on the line **above** the edit, exactly this form (note the em dash):

```go
// xollama-hook: <feature-id> — see docs/features/<feature-id>.md
if engine.Selected() == engine.Opencoti {
	return engine.FindServer(gpus)
}
```

Rules for the edit itself:

- One marker per hook site. Two call sites for one feature = two markers, one Registry row per site.
- The `if` must fall through to the untouched upstream code path when the feature is off — no `else` that rewrites upstream behaviour.
- Do not touch surrounding lines. `git diff` for the file should show only your added lines plus the marker.
- An added import goes in the existing import block, no marker needed on the import if the call site is marked.

**Verify before Step 5:**

```sh
git diff v0.34.2 -- <path>   # only your added lines, nothing reflowed
gofmt -l <path>              # prints nothing
```

### Step 5 — Add the Registry row in the same commit

Append to the `## Registry — known surgical hooks` table at the bottom of `docs/protocols/UPSTREAM-SYNC.md`. Replace the `_(none yet — scaffolding only)_` placeholder row the first time:

```markdown
| Hook ID | File | Feature | Plan |
|---|---|---|---|
| engine-opencoti-llamafile | `llm/llama_server.go` | route GGUF to opencoti-llamafile when `XOLLAMA_ENGINE=opencoti` | [`docs/features/engine-opencoti-llamafile.md`](../features/engine-opencoti-llamafile.md) |
```

Hook ID column = the feature id string that appears after `xollama-hook:` in the code, character for character.

**Verify before Step 6** — reconcile markers against the Registry (the protocol names a check script that is not written yet; use this pipeline, it is what the script must do):

```sh
grep -rho "xollama-hook: [a-z0-9-]*" --include='*.go' . | sed 's/.*: //' | sort -u > /tmp/hooks-markers.txt
sed -n '/^## Registry/,$p' docs/protocols/UPSTREAM-SYNC.md | awk -F'|' 'NF>3 && $2 !~ /^ *-+ *$/ {gsub(/^ +| +$/,"",$2); if ($2 ~ /^[a-z0-9-]+$/) print $2}' | sort -u > /tmp/hooks-registry.txt
diff /tmp/hooks-markers.txt /tmp/hooks-registry.txt && echo "IN SYNC"
```

Must print `IN SYNC`. (`/tmp` only — these are <1KB ephemeral files.)

### Step 6 — Build, vet, test

In this order, and do not skip one because the previous passed:

```sh
go build ./...
go vet ./...
go test ./llm/... ./server/... ./model/... ./thinking/...
golangci-lint run
```

Go-only iteration needs no native rebuild:

```sh
go build .
go run . serve
```

runs against the existing payload.

**Verify before Step 7:** all four commands exit 0.

### Step 7 — Prove "off means off"

```sh
XOLLAMA_ENGINE=llamacpp go run . serve   # then exercise the touched path
```

Compare against the same request on a build of the v0.34.2 tag. For a hook on the runner path, boot both engines against one model and diff the responses, as the sync workflow does. Record the result in the plan doc from Step 2 under a "measured" heading — the same way `docs/evaluations/phase0-engine-compat.md` records its numbers.

**Verify before Step 8:** with the feature off, the output is identical to upstream's. If it is not, the gate is wrong — fix the gate, do not document the difference.

### Step 8 — Commit

One commit contains: the additive helper, the marked edit, the Registry row, the plan doc. Message format is upstream's (`CONTRIBUTING.md`): `<package>: <short description>`, lowercase, continuing "This changes Ollama to...".

```
llm: route GGUF to opencoti-llamafile behind XOLLAMA_ENGINE

Surgical hook at FindLlamaServer; inert with XOLLAMA_ENGINE=llamacpp.
Registry row added in docs/protocols/UPSTREAM-SYNC.md.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
```

Secret scan runs from `.githooks/pre-commit`; run it manually if committing with `--no-verify` is ever needed:

```sh
gitleaks protect --staged --config .gitleaks.toml
```

### Step 9 — OpenWolf bookkeeping

- New files → add entries to `.wolf/anatomy.md`.
- Append one line to `.wolf/memory.md`: `| HH:MM | hook <feature-id> at <file> | <files> | passed | ~Ntokens |`.
- Any error hit on the way → append to `.wolf/buglog.json` with `error_message`, `root_cause`, `fix`, `tags`.
- Any new convention or user correction → `.wolf/cerebrum.md`.

## Examples

### Example 1 — a real hook

**User says:** "Make `llm/llama_server.go` pick the opencoti binary when `XOLLAMA_ENGINE=opencoti`."

**Actions taken:**

1. ```sh
   git cat-file -e v0.34.2:llm/llama_server.go
   ```
   exits 0 → upstream file, hook path.
2. Triage: not additive (the resolver must be called from upstream's `FindLlamaServer`); not a carried patch (ollama will not take an engine swap). → **surgical hook**.
3. Plan `docs/features/engine-opencoti-llamafile.md` already exists; feature id = `engine-opencoti-llamafile`.
4. New additive package under `llm/` (resolver.go, `package engine`) with `Selected() Engine` and `FindServer(gpus) (string, error)`, plus its sibling test file. `go test ./llm/engine/...` passes.
5. A new additive file in `envconfig/`, alongside `envconfig/config.go`, adds `XollamaEngine()` over `Var("XOLLAMA_ENGINE")`, defaulting to `"llamacpp"`.
6. Three added lines in `FindLlamaServer` (`llm/llama_server.go`), marker on top:
   ```go
   // xollama-hook: engine-opencoti-llamafile — see docs/features/engine-opencoti-llamafile.md
   if engine.Selected() == engine.Opencoti {
       return engine.FindServer(gpus)
   }
   ```
7. Registry row added to `docs/protocols/UPSTREAM-SYNC.md`; reconciliation pipeline prints `IN SYNC`.
8. ```sh
   go build ./... && go vet ./... && go test ./llm/... ./server/...
   ```
   → green. A/B with `XOLLAMA_ENGINE=llamacpp` matches the v0.34.2 build's output.
9. One commit: `llm: route GGUF to opencoti-llamafile behind XOLLAMA_ENGINE`.

**Result:** one 3-line diff against upstream in `llm/llama_server.go`, one Registry row, everything else additive. The next `git merge upstream/main` conflicts on three lines at most, and the reconciliation check can prove the hook is accounted for.

### Example 2 — the triage that avoids a hook

**User says:** "Fix the repetition guard, it aborts on base64 payloads."

**Actions taken:** This is a bug ollama would take → **carried patch**, not a hook. Branch `up-repeat-guard` on the fork remote (mann1x/ollama), upstream PR #17563, logic in the additive `llm/repeat_guard.go` + `llm/repeat_guard_test.go`, minimal edits in `llm/llama_server.go` and `llm/server.go` written in upstream's own style with **no** `xollama-hook:` marker, merged onto `main` with `--no-ff`, row added to Tier 2 of `docs/protocols/CARRIED-PATCHES.md`.

**Result:** retiring it later is one revert of an identifiable commit range, the day upstream merges the PR.

## Common Issues

**`fatal: Not a valid object name`** from the `git cat-file` probe — the file does not exist upstream. This is an additive file: write it normally, no marker, no Registry row. Do not add a marker "for consistency" — the reconciliation check counts markers, and an orphan marker on an additive file is noise.

**`diff` in Step 5 prints a marker with no Registry row** — add the row to the `## Registry` table in `docs/protocols/UPSTREAM-SYNC.md` and amend it into the *same* commit:

```sh
git add docs/protocols/UPSTREAM-SYNC.md && git commit --amend --no-edit
```

**`diff` prints a Registry row with no marker** — either the hook was reverted (delete the row) or the marker text drifted. Marker and row must match character for character; check for a hyphen/underscore mismatch and for the em dash `—` (not `--`) in `// xollama-hook: <id> — see ...`.

**The hook-reconciliation script is missing** — `docs/protocols/UPSTREAM-SYNC.md` names it but it is not written yet. Use the grep/awk pipeline in Step 5. If asked to create the script, it must implement exactly that pipeline and exit non-zero on any diff.

**`go build ./...` reports the module does not contain a package**, or imports break after a rename attempt — the module path was changed. Revert `go.mod` to upstream's module line and revert every rewritten import. The binary name is set at build time; the import path is upstream's.

**`unknown parameter '%s'`** from `api/types.go` at model create time — ollama rejects unknown Modelfile `PARAMETER` keys before any push. xollama configuration cannot ride on `PARAMETER`; use an `XOLLAMA_*` env var through `envconfig.Var` in `envconfig/config.go` instead.

**Merge conflict in `llm/llama_server.go` / `llm/server.go` / `model/parsers/` during a later sync** — a clean resolve is not evidence of a correct one. Resolve toward the carried PR's intent, then read `git diff main...HEAD` over the conflicted path (the diff against our trunk, not the merge diff) and re-run:

```sh
go test ./llm/... ./model/parsers/... ./thinking/...
```

Record the reasoning in the merge commit message, as `1e384e33` does.

**Behaviour differs with `XOLLAMA_ENGINE=llamacpp`** — the gate is missing or inverted. Check the hook is `if <feature on> { ... }` with fall-through, not an `else` branch or a changed default. Recheck that `envconfig.Var("XOLLAMA_ENGINE")` defaults to `llamacpp` when unset, and remember there are ~7 direct `os.Getenv` bypasses in `app/`, bench and UI code that do not go through `Var`.

**`golangci-lint run` flags an upstream file you barely touched** — do not fix the pre-existing lint. Narrow your edit until only your lines are reported; upstream-owned lint belongs in an upstream PR.
