---
name: carried-patch-workflow
description: Stages, merges, verifies and retires a carried upstream ollama PR in the xollama soft fork: creates a carry/<slug> branch, merges the fork remote's up-<slug> PR branch with --no-ff using the project's exact merge-commit format, resolves conflicts toward the PR, runs the go build/vet/test gate, updates docs/protocols/CARRIED-PATCHES.md, and lands on main with --ff-only. Use when the user says 'carry PR #NNNNN', 'merge the carried patch', 'rebase the carried patches', 'upstream merged our PR', 'retire a patch', 're-check the carried patches after a sync', or when work is happening on a carry/* branch such as carry/think-budget or carry/repeat-guard. Capabilities: PR-branch staging on the fork remote (mann1x/ollama), --no-ff merge with the Carries/Upstream PR trailer block, conflict-resolution rules for llm/ and model/parsers/, CARRIED-PATCHES.md table maintenance, and retirement by git revert -m 1. Do NOT use for ordinary xollama feature work with no upstream PR (that is an additive file plus docs/features/ and a surgical hook in the UPSTREAM-SYNC Registry), do NOT use for a full upstream version sync (follow docs/protocols/UPSTREAM-SYNC.md instead), and do NOT use for authoring the upstream PR's code itself.
paths:
  - docs/protocols/CARRIED-PATCHES.md
  - docs/protocols/UPSTREAM-SYNC.md
---
# Carried Patch Workflow

A **carried patch** is a change xollama ships that is *also* an open PR against
upstream ollama. It lives here only until upstream takes it. The contract is
`docs/protocols/CARRIED-PATCHES.md`; this skill is how that contract is executed.

## Critical

Read these before touching anything. Violating one of them is not a style
miss — it silently corrupts the next upstream sync.

1. **One patch = one `--no-ff` merge commit.** Never squash, never rebase a
   carried patch onto `main`, never merge two PRs in one commit. Retirement is
   `git revert -m 1 <merge-sha>`, and that only works if the merge is one
   identifiable range.
2. **Never rename the Go module path.** It stays upstream's, on the first line
   of `go.mod`. The binary is xollama; the import path is upstream's.
3. **The upstream PR is the source of truth.** Fixes go to the PR branch on the
   fork remote (mann1x/ollama) *first*, then flow here as a new merge. Never
   fix a carried patch only on `main` — the next PR push loses it.
4. **A carried patch is NOT a surgical hook.** Do not add a
   `// xollama-hook:` marker and do not add a row to the Registry in
   `docs/protocols/UPSTREAM-SYNC.md`. Those are for xollama-only edits.
   Carried patches are upstream-bound, so they are also exempt from the
   "off means off" gating rule — they change behaviour unconditionally,
   exactly as the PR does.
5. **No upstream PR ⇒ not a carried patch.** It is an xollama feature: an
   additive file, a plan doc under `docs/features/`, and a gated hook. If the
   user asks to "carry" something with no PR number, say so and stop.
6. **A PR that merged upstream is retired the same day.** Carrying it after
   upstream took it is how a silent double-apply happens.
7. **Never merge onto `main` directly.** A half-resolved merge on the trunk is
   hard to back out of. Work on a carry/… branch, land with `--ff-only`.

## Naming, fixed by convention

| Thing | Form | Real example |
|---|---|---|
| Local integration branch | carry/&lt;slug&gt; | carry/repeat-guard |
| PR branch on the fork remote | the **Branch** column of `docs/protocols/CARRIED-PATCHES.md`, usually up-&lt;slug&gt; | up-repeat-guard |
| Merge subject | `Merge PR #NNNNN — <PR title, lowercase sentence>` | `Merge PR #17563 — stop guessing that a repetitive payload is a runaway` |

Older PR branches predate the `up-` prefix (gemma4-toolcall-in-thinking,
qwen3coder-tolerate-malformed-tool-calls). **Always read the Branch column
verbatim — never guess the prefix.**

Remotes: `origin` = mann1x/xollama · `upstream` = ollama/ollama ·
`fork` = mann1x/ollama.

## Instructions

### Step 1 — Identify the patch and confirm it is still open

```sh
cd /srv/dev-disk-by-label-opt/dev/xollama
grep -n '#NNNNN' docs/protocols/CARRIED-PATCHES.md   # tier, title, branch name
gh pr view NNNNN --repo ollama/ollama --json state,title,headRefName,mergedAt
```

- `state: OPEN` → continue to Step 2.
- `state: MERGED` → this is a **retirement**, jump to Step 8.
- `state: CLOSED` (not merged) → stop and ask the user whether to keep carrying
  it; a closed-unmerged PR is no longer a carried patch by Rule 4 of
  `docs/protocols/CARRIED-PATCHES.md`.

Verify you have the PR number, the exact PR title, and the Branch column value
before proceeding.

### Step 2 — Start from a clean trunk

```sh
git status --short          # must be empty; stash or commit anything else first
git fetch fork upstream
git checkout main && git pull --ff-only origin main
git log --oneline --first-parent -5
```

Verify `git status --short` prints nothing and `main` matches `origin/main`
before proceeding. If a previous merge is still in progress (`UU` lines, or
`.git/MERGE_HEAD` exists), finish or `git merge --abort` it first — do not
start a second carry on top of a conflicted tree.

### Step 3 — Create the carry branch

Uses the slug from Step 1.

```sh
git checkout -b carry/<slug>
git rev-parse --abbrev-ref HEAD
```

Verify the second command prints the carry branch and **not** `main` before
proceeding to the merge.

### Step 4 — Inspect the merge before making it

Uses the PR branch name from Step 1.

```sh
git log --oneline main..fork/up-<slug>            # commits the PR adds
git diff --stat main...fork/up-<slug>             # files it touches
git rev-list --count $(git merge-base main fork/up-<slug>)..main
```

That last count is how far behind the PR's base is — the think-budget branch was
23 upstream commits behind the v0.34.2 tag, which is precisely why its merge
produced a silent breakage. If the count is non-zero, expect conflicts in the
files the stat list shares with recent upstream commits.

Verify you can name the files that will conflict before proceeding.

### Step 5 — Merge with `--no-ff`

```sh
git merge --no-ff --no-commit fork/up-<slug>
```

Resolve conflicts with these rules:

- **Resolve toward the PR** on any line the PR exists to change. Real precedent:
  in `llm/llama_server.go`, upstream's crude `tokenRepeat > 100` abort is what
  PR #17563 replaces — keeping upstream's side would not even have compiled,
  because the declaration was removed in a hunk that merged cleanly.
- **Keep upstream's side** for API/machinery migrations unrelated to the PR's
  intent, then adapt the PR's code to them. Precedent: upstream moved
  `server/routes_test.go` from `fs/ggml` to `fs/gguf` + `internal/testutil`;
  the PR's new test had to be rewritten to `gguftest.KV` to match every other
  call site in that file.
- `model/parsers/`, `thinking/`, `llm/llama_server.go`, `llm/server.go`,
  `server/routes.go` are **read line by line**. A clean auto-resolve there is
  not evidence of a correct one: upstream's parser edits and ours touch the same
  lines for unrelated reasons.

Verify `git diff --check` is clean and `git status --short` shows no `UU`
entries before proceeding.

### Step 6 — Run the verification gate, in this order

Do not skip one because the previous passed. `go build` is not sufficient —
the think-budget breakage compiled fine and only `go vet` caught it.

```sh
go build ./...
go vet ./...
go test ./llm/... ./server/... ./model/parsers/... ./model/renderers/... ./thinking/... ./harmony/... ./api/... ./openai/... ./anthropic/... ./middleware/...
golangci-lint run
```

Known-unrelated failure: `cmd/launch/TestCodexAppCountsOnlyOllamaRequestsInRegularProfile`
fails identically on a clean build of the v0.34.2 tag. It is upstream's, not the
patch's — note it, do not fix it here.

If a test fails, fix it **on the PR branch first** (Critical rule 3), push to
the fork remote, then redo the merge. Verify all four commands pass before
proceeding.

### Step 7 — Commit the merge, land it, push

Uses the title from Step 1 and the conflict notes from Step 5. The body is
fixed: a `Carries …` line, the PR URL, then any conflict/verification notes.

```sh
git commit --no-edit -m "$(cat <<'EOF'
Merge PR #17563 — stop guessing that a repetitive payload is a runaway

Carries up-repeat-guard from the fork remote onto v0.34.2.
Upstream PR: https://github.com/ollama/ollama/pull/17563

Conflict in llm/llama_server.go, resolved toward the PR. Upstream's crude
`tokenRepeat > 100` abort is exactly what this PR replaces, and its declaration
was already removed in a hunk that merged cleanly — keeping upstream's side
would not have compiled.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"

git checkout main
git merge --ff-only carry/<slug>
```

The `.githooks/pre-commit` hook runs:

```sh
gitleaks protect --staged --config .gitleaks.toml
```

Let it run, never `--no-verify`.

**Confirm with the user before pushing** (`git push origin main`,
`git push fork up-<slug>`) — pushing is outward-facing and not implied by
"merge the patch".

Verify `git log --oneline --first-parent -3` shows the merge as the tip of
`main` before proceeding.

### Step 8 — Update `docs/protocols/CARRIED-PATCHES.md`

In the **same landing** as the merge, edit the tier table row:

```markdown
| [#17563](https://github.com/ollama/ollama/pull/17563) | `llm`: stop guessing that a repetitive payload is a runaway | `up-repeat-guard` | 2026-08-04 | 45d |
```

- Tier 1 = the reason the fork exists · Tier 2 = correctness fixes ·
  Tier 3 = platform.
- Title is the PR title, `package`-prefixed in backticks, lowercase after the colon.
- `Age` = today − Opened, in days. Recompute every row you touch and update the
  `Status as of **YYYY-MM-DD**` line and the "All twelve are OPEN" count sentence.

**Retiring** (PR merged upstream):

```sh
git checkout -b retire/<slug>
git revert -m 1 <merge-sha>          # -m 1 keeps main's line, drops the patch
go build ./... && go vet ./... && go test ./llm/... ./server/... ./model/parsers/... ./thinking/...
```

Then delete the row from the table, drop the count sentence by one, and delete
the stale branch with `git branch -d carry/<slug>`. Commit subject:
`docs: retire PR #NNNNN — upstream merged it`.

Verify the reverted files match upstream's version (`git diff upstream/main -- <files>`
shows only unrelated xollama changes) before landing.

### Step 9 — OpenWolf bookkeeping

1. Append to `.wolf/memory.md`: `| HH:MM | carried PR #NNNNN onto main | docs/protocols/CARRIED-PATCHES.md | merged, tests pass | ~Ntokens |`
2. If a conflict resolution broke the build or a test, append an entry to
   `.wolf/buglog.json` with `error_message`, `root_cause`, `fix`, and tags
   `["carried-patch", "upstream-merge", "<slug>"]`.
3. If the merge taught a rule that would trip a fresh session (a file where
   upstream and the PR always collide), add it to `.wolf/cerebrum.md`
   under `## Key Learnings`.

## Examples

### Carrying an open PR

**User says:** "carry PR #18288"

**Actions taken:**
1. `grep -n '18288' docs/protocols/CARRIED-PATCHES.md` → Tier 2, branch
   up-gemma4-stray-closer, title "`parsers`: drop an unmatched thinking close
   tag instead of leaking it".
2. `gh pr view 18288 --repo ollama/ollama --json state` → `OPEN`.
3. ```sh
   git fetch fork
   git checkout main && git pull --ff-only
   git checkout -b carry/gemma4-stray-closer
   ```
4. `git diff --stat main...fork/up-gemma4-stray-closer` → `model/parsers/gemma4.go`,
   `model/parsers/gemma4_test.go`. A read-line-by-line path.
5. `git merge --no-ff --no-commit fork/up-gemma4-stray-closer`, resolve the
   parser conflict toward the PR.
6. `go build ./... && go vet ./... && go test ./model/parsers/... ./thinking/...` → pass.
7. Commit with `Merge PR #18288 — drop an unmatched thinking close tag instead of
   leaking it`, body `Carries up-gemma4-stray-closer from the fork remote onto v0.34.2.`
   + PR URL + `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
8. `git checkout main && git merge --ff-only carry/gemma4-stray-closer`.
9. Recompute the Age column, append to `.wolf/memory.md`, ask before pushing.

**Result:** `main` gains one `--no-ff` merge commit, revertible in one command;
`git log --oneline --first-parent` reads as a list of carried PRs.

### Upstream took a patch

**User says:** "upstream merged #17567"

**Actions taken:** `gh pr view 17567 --repo ollama/ollama --json state,mergedAt`
confirms `MERGED` → `git log --oneline --first-parent --grep 'PR #17567'` finds
the merge sha → `git checkout -b retire/mlx-libdl && git revert -m 1 <sha>` →
build/vet/test → delete the Tier 3 row from `docs/protocols/CARRIED-PATCHES.md`,
drop the count, `git branch -d carry/mlx-libdl`.

**Result:** the patch is carried exactly once — by upstream — and the revert is
a single identifiable commit.

## Common Issues

**`fatal: 'fork/up-<slug>' - not a valid object name`**
1. `git fetch fork` — the ref is stale.
2. `git branch -r | grep fork/` and match against the Branch column of
   `docs/protocols/CARRIED-PATCHES.md`; older PRs have no `up-` prefix. Note the
   fork also holds both a bare and an `up-`prefixed repeat-guard branch — the
   Branch column names the live one.

**`fatal: Not possible to fast-forward, aborting` on the `--ff-only` landing**
`main` moved while you worked. Do **not** rebase the carry branch — that
destroys the merge commit. Instead:

```sh
git checkout carry/<slug> && git merge main
```

re-run the Step 6 gate, then retry the `--ff-only`.

**`go build ./...` passes but the package does not compile in tests**
This is the think-budget failure mode. `go vet ./...` catches it; always
run vet. Typical cause: the PR's new test calls an upstream helper that moved
(`ggml.KV` → `gguftest.KV`). Fix the PR's code to the new call site, matching
the other call sites in the same file.

**`git revert -m 1` reports `mainline was specified but commit is not a merge`**
You grabbed a commit from inside the PR branch, not the merge commit. Find the
right one with `git log --oneline --first-parent main --grep 'PR #NNNNN'`.

**The hook-reconciliation script named by `docs/protocols/UPSTREAM-SYNC.md` is missing**
Step 3 of that protocol references it but it is not written yet. Carried patches
carry no `xollama-hook:` markers anyway, so this step does not apply to this
workflow — skip it and note the gap; do not invent the script as part of a carry.

**Merge leaves `UU` entries you did not expect in `llm/llama_server.go`**
Expected on this file — it is both the surface the carried patches change and
the surface upstream edits most. Resolve toward the PR for the PR's own
behaviour, and record *why* in the merge commit body, as merge `1e384e33` does.

**A conflicted merge is already in progress when you start**
`git status` shows `UU` lines and `.git/MERGE_HEAD` exists. Ask the user whether
to finish or abort it — never `git merge --abort` someone else's in-flight
resolution without confirming.
