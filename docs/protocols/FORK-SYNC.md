# Fork sync — how xollama and `mann1x/ollama` stay in step

The contract between two repositories worked on by two sessions:

| | |
|---|---|
| **xollama** | `mann1x/xollama` — this repo. A soft fork of ollama; consumes patches. |
| **the fork** | `mann1x/ollama` — where patches are authored and staged as upstream PRs. |
| **upstream** | `ollama/ollama` — the eventual destination, which must be assumed never to merge. |

Agreed 2026-09-21 between the `xollama@solidpc` and `ollama@solidpc` sessions,
proposed by the latter. `docs/protocols/CARRIED-PATCHES.md` records *which*
patches this repo carries and at which merge; this file records *how* they get
here and stays the shared definition. Both repos reference it.

## Why it exists

Not theory. On 2026-09-21 `model/parsers/gemma4.go` was three different things
across two trees:

| | xollama `dev` | fork `think-budget` |
|---|---|---|
| `ToolCallTags()` | **absent** | present |
| stray channel-name strip | single `CutPrefix`, one orphan | loop over both orphans |
| thinking-state tool-call fix | present | present |

One patch at its current revision, one at an older revision, one missing
entirely. Nobody did anything wrong. There was simply **no statement anywhere of
what the patch set is and at which sha**, so each tree took whatever was visible
when someone last looked. Our own `CARRIED-PATCHES.md` even listed
`up-gemma4-stray-channel-name` as carried when it had never been merged.

Upstream merging is not the fix. Twelve fork PRs have sat on `ollama/ollama`
since August with no maintainer review, which is the reason the fork exists at
all. **The protocol assumes upstream never merges.**

## The rules

**R1 — One home per patch.** Every fix lives on exactly one branch in
`mann1x/ollama`, named `up-<slug>`, whose head *is* the head of its upstream PR.
A patch is never authored anywhere else.

> **A branch that is the head of an open cross-fork PR keeps the name it has.**
> Renaming it **closes the PR**. GitHub retargets open PRs on a rename *within*
> a repo; it does not do so when the head lives in a fork, and `gh pr reopen`
> then refuses, because the old ref survives only as a redirect and not as a
> real branch. Measured 2026-09-21 on upstream #18307: the rename to
> `up-gemma4-toolcall-in-thinking` closed it, and recovery cost recreating the
> branch, reopening the PR and deleting the duplicate — a close/reopen pair now
> sits in its timeline. Such a branch takes the `up-` prefix **only once its PR
> is closed or merged.**

So two of the twelve carry no prefix on purpose:
`gemma4-toolcall-in-thinking` (#18307) and
`qwen3coder-tolerate-malformed-tool-calls` (#17914). The missing prefix is also
what hid them — #17914 never appeared in an `up-*` survey for exactly that
reason, the same way #18307 went astray. **Survey the manifest, never the
branch names.**

**R2 — `think-budget` is integration only.** Every commit on it is a
`cherry-pick -x` from an `up-*` branch, or CI/notes/manifest. Releases are cut
from it and nowhere else. No patch is authored directly on it.

**R3 — `PATCHES.json` is the contract.** At the root of `think-budget`,
fetchable raw:

```
https://raw.githubusercontent.com/mann1x/ollama/think-budget/PATCHES.json
```

```json
{
  "base": "v0.34.2",
  "updated": "2026-09-21",
  "integration": { "branch": "think-budget", "sha": "3af5f361",
                   "release": { "tag": "v0.34.2-1-thinkbudget", "sha": "645ec440" } },
  "patches": [
    { "slug": "gemma4-toolcall-in-thinking",
      "branch": "gemma4-toolcall-in-thinking",
      "sha": "16a78556",
      "upstream_pr": 18307,
      "rebased_onto": "v0.34.2",
      "files": ["model/parsers/gemma4.go", "model/parsers/gemma4_test.go"],
      "status": "open-upstream" }
  ],
  "known_conflicts": [ ... ],
  "verification": { "per_patch": "...", "apply_order": "..." }
}
```

`integration.sha` is the tree the patch set describes; the manifest commit is
its child. `known_conflicts` names the collisions that are **not** ordering
mistakes — two additive test blocks landing at the same point conflict in
either direction — and carries the resolution, so a merge conflict can be
checked against it instead of guessed at.

It is bumped in the same commit that changes what is applied. One `curl`
answers "what is the patch set, at which sha" with no guessing from PR
timestamps. **`patches[]` order is apply order** — xollama merges each as its
own `--no-ff` merge, and a wrong order is a silent conflict resolution rather
than an error.

**R4 — Consume by sha, never by hand-copy.** xollama merges `up-<slug>` at the
sha the manifest names. If a patch needs changing *for* xollama, the change goes
onto the `up-*` branch first and returns through a manifest bump. A fix authored
inside xollama, on a file both repos own, is exactly how the `gemma4.go` split
above happened.

**R5 — A mail on every manifest bump**, to `xollama@solidpc`, naming the base
tag and the shas that moved. Neither side polls.

**R6 — Rebase cadence.** On every upstream tag we build against, all `up-*`
branches are rebased onto it, the manifest is republished with the new `base`,
and the mail goes out. One event to track instead of ten branches.

**R7 — `mann1x/ollama@main` mirrors upstream, plus the release workflow.** It
was six weeks stale, which is what made every fork PR a 451-file diff burying
the two files that mattered. Mirrored, it is never a development target and
never a base anyone reasons about.

> **`.github/workflows/thinkbudget-release.yaml` must stay on `main`.** GitHub
> discovers a `workflow_dispatch` workflow **only on the default branch**, so
> mirroring it away put the workflow into state `deleted` and the fork could not
> cut a release at all — silently, until the next release. `main`'s copy is
> `think-budget`'s, byte for byte, which also closes the opposite trap: `main`
> once held a four-job version against the branch's seven, so a dispatch that
> forgot `--ref` produced a *green* run missing two release assets. Keep the two
> copies in step; dispatch stays `--ref think-budget`.

> **Done 2026-09-21.** `main` moved from `eef55508` (2026-08-10) to upstream
> `6383a0fa` plus that one file, at `a4aed68b` — verified here against the
> GitHub API: one changed file, parent `6383a0fa`, workflow state `active`. The
> pre-mirror `main` is kept as `main-pre-mirror-20260921` at `eef55508`. The
> payoff is measured: fork PR #1 (`think-budget` → `main`) went from a 451-file
> diff to 66 files, `+6179/-311`.
>
> Fifteen commits were on the old `main` and not upstream; two had no `up-*`
> home, and both were checked before the overwrite rather than after —
> `b002feda` is obsolete (its file does not exist at v0.34.2) and `f05a6f76`
> already lives on `think-budget`. An untracked patch that still applied would
> have been given an `up-*` branch first. That is R1 doing its job.

> **xollama fetches nothing from `mann1x/ollama@main`.** Verified 2026-09-21:
> no build file, cmake module, pin, workflow or Go source references
> `mann1x/ollama` at all. We touch the `fork` remote only to fetch `up-*`
> branches for merges. Mirroring `main` cost this repo nothing — which is *why*
> the mirror was safe to do, so this verification is load-bearing in both
> directions.

## The rebase base is the upstream TAG

`up-*` branches are rebased onto **the upstream release tag xollama builds**,
currently `v0.34.2` — never upstream `main`.

This repo's `main` is upstream release v0.34.2 plus fork changes, carrying the
full upstream history so every `git merge upstream/main` has a real merge-base.
A patch rebased onto upstream `main` drags unreleased upstream into that
merge-base and costs the ~33% per-sync conflict rate the fork was restructured
to avoid (measured: 155 of the 474 files upstream touched between v0.34.0 and
v0.34.2). When xollama moves to a new upstream tag, the fork is told **before**
the rebase, not after.

## Dependent patches — the one amendment to "based on the upstream tag"

Decided 2026-09-21, on the first patch that could not satisfy the rule.

`up-*` branches rebase onto the upstream tag, and twelve of fourteen do. The
exception is a patch whose changes **cannot exist** on that tag. The case that
forced it: the reader half of the response-scope thinking budget edits
`llama/compat/004-reasoning-budget-line-boundary.patch`, a file that does not
exist at `v0.34.2` because `up-reasoning-budget-line-boundary` (#18212) creates
it, and it needs `up-think-budget`'s (#17566) Go plumbing besides. Its only
honest base is a tree carrying both.

**The amendment.** Such a patch gets a stacked `up-*` branch and declares its
dependencies:

- the manifest row carries `depends_on` naming them, and a `base` that is the
  dependency set rather than the tag;
- the branch is built on the merge of exactly those dependencies, **at their
  manifest shas**;
- it comes **after** all of them in `patches[]`, which apply order already
  guarantees once it is placed there;
- it is `fork-only`. Not by preference — a patch that only applies on top of two
  unmerged PRs cannot be a standalone upstream PR.

**What this changes on xollama's side.** The invariant checked before merging
was "every branch's merge-base is the upstream tag". It becomes: the merge-base
is the tag, **or** every entry in `depends_on` is earlier in `patches[]` and the
branch's merge-base lies within their merge. A branch that satisfies neither is
not merged and the fork is asked, as before.

**Why not the alternatives**, since both were offered and one looked cheaper:

- *Fold it into `up-think-budget` and reorder so #18212 precedes it.* This does
  not actually avoid the amendment. The branch would still have to edit a file
  that does not exist at its base, so either it stops being tag-based anyway, or
  it absorbs #18212's commit — which puts one open upstream PR's changes inside
  another's. #17566 is the headline PR and the one most likely to be read; the
  whole point of mirroring `main` was to stop fork PRs being unreadable.
- *Leave it fork-only on `think-budget` with a note.* That is precisely the
  state that produced the `ToolCallTags()` finding: code on the integration
  branch with no home, which R2 forbids and which nobody notices until someone
  greps for it. Choosing it knowingly is worse than having arrived there by
  accident.

## What xollama does on receipt of a manifest bump

1. `git fetch fork` and merge each named `up-<slug>` **at the manifest's sha**,
   in `patches[]` order, each as its own `--no-ff` merge — so retiring one stays
   a single revert of an identifiable range.
2. Update `docs/protocols/CARRIED-PATCHES.md` in the same commit: the upstream
   PR number, the branch, and **our** merge sha.
3. Reply with those merge shas, so the fork's manifest and this registry can be
   reconciled from either end.
4. Never hand-copy a hunk from `think-budget`. If something is needed before the
   manifest exists, ask for the branch and sha.

## Identifiers

The **upstream PR number** is the single identifier for a patch, on both sides.
`CARRIED-PATCHES.md` already keys off it and `PATCHES.json` carries it as
`upstream_pr`. The fork-internal PRs on `mann1x/ollama` that duplicated the
upstream ones — #3, #4, #5, #6, #7 — are **closed** as of 2026-09-21. #1 stays
open: it is where the repository owner reviews `think-budget` itself, and it is
not a review surface for this repo either.

`#16820` (`/api/tokenize`) is carried here but is **not in the manifest** and
never will be: it is somebody else's upstream PR, merged from `pull/16820/head`,
so no branch in `mann1x/ollama` is its home. It is tracked only by
`CARRIED-PATCHES.md`.

## The drift on `004-reasoning-budget-line-boundary.patch`

Measured here on 2026-09-22, not taken on report, because the claim that
`up-reasoning-budget-line-boundary` (#18212) was a superset turned out to be
true only of the residue:

| | #18212 | `think-budget` | this tree |
|---|---|---|---|
| lines in the patch | 825 | 945 | 825 |
| touches `common/arg.cpp` | yes | **no** | yes |
| `reasoning-budget-scope`, `THINK_BUDGET_SCOPE` | 2, 1 | 0, 0 | 2, 1 |
| `forced` | 33 | **62** | 33 |
| `end_offset`, `_apply`, `reset_tokens` | 0, 0, 0 | **2, 3, 10** | 0, 0, 0 |

So the two have drifted in **both** directions, and this tree has #18212's side
of it exactly. What we do not have is `b4c58eee` — the fix for a spent response
repeating its wrap-up message, measured by the fork at 32 identical 240-character
copies in one turn. Its identifiers (`forced_end_pos`,
`common_reasoning_budget_end_offset`, `common_reasoning_budget_apply`) appear
nowhere on #18212 under any spelling, so it is genuinely absent rather than
renamed.

**Ruling: the llama.cpp half belongs on `up-response-scope-think-budget`, not
on #18212 and not left on `think-budget`.** That branch is already the fork-only
home of this feature's Go half, already stacked on #4+#8+#14, and already last in
apply order. Putting the C++ there closes the drift without rewriting an open
upstream PR's head, and leaves #18212 as what it should be: the upstream-facing
patch, with the `--reasoning-budget-scope` CLI flag that ollama does not use
because it sets the scope per request over the wire.

Two things follow:

- `ca2e2cdb` ("port to llama.cpp b10242") is almost certainly obsolete, the way
  `b002feda` was. This tree pins **b10969** (`LLAMA_CPP_VERSION`), and #18212's
  patch is the one that applies to it. Check it against the pin before carrying
  it; do not port backwards.
- Once the C++ moves, `think-budget`'s copy of the file must equal the replay of
  #18212 plus that branch's delta. That is checkable with a diff, and it is the
  test that says the drift is actually closed rather than moved.

**This cannot be verified by the Go gates.** A `.patch` is applied by
`cmake/apply-git-patches.cmake` through `FetchContent`'s `PATCH_COMMAND`; every
Go check passes on a tree whose patch is mangled. Worse, the applier is
idempotent: it skips anything `git apply --reverse --check` accepts, so on a
tree that already carries the old version of a patch the new one is skipped
rather than rejected. Only a **clean fetch** verifies it.

**Done, 2026-09-22.** Clean fetch of revision `391fac164` (b10969) applied
`001`, `models/003`, `004` and `005`; the union was checked by identifier, not
by line count. The end-to-end repro then ran on the 3090 in two arms differing
only in `llama-server`, `libllama-common.so*` and `libllama-server-impl.so`:
**thirteen before-arm runs across two models, seven budgets and two prompts
written to coax a reopen, and every one produced exactly one wrap-up copy.** The
32-copies behaviour does not reproduce here. That is not a refutation of
`b4c58eee` — no model reopened its thinking block, so the repro never reached
the path the fix guards — but it is what the fork asked to be told. Numbers,
arm provenance and the lane in `CARRIED-PATCHES.md`.

## Open items

- **Manifest at 18 patches**, all merged here; shas in `CARRIED-PATCHES.md`.
  The first stacked patch (`up-response-scope-think-budget`) exercised the
  amendment and the widened invariant held.
- **Three orphans found by looking at one feature**, all on `think-budget` with
  no `up-*` home: `677f91ce`, `b4c58eee`, `ca2e2cdb`. The fork has offered to
  sweep `think-budget` for every commit touching a file no branch carries —
  **accepted**. R2 stops new orphans; it does not find the ones already there.
- **Orphans point both ways.** `060144ab` was authored in xollama on an upstream
  file and never sent to the fork. The sweep should have a counterpart here: any
  xollama commit touching a file that exists at `v0.34.2` and is not part of a
  marked hook is a candidate for the same treatment.
- **The 004 end-to-end verification is closed** (2026-09-22), with a null
  result over two rounds: 19 before-arm runs across three models and both reset
  routes, one wrap-up copy every time. The fork supplied their recorded
  parameters and a structural reason the first round could not have reached the
  guard — verified here, and correct. The second round used their own tag
  (`qwen3.8-mtp_tb:27b-q4km`, `parser qwen3.5`) with 20–29 tool calls per run,
  and still no second thinking block opens, so the guard's precondition never
  occurs. **Two open questions, both theirs:** does their 08-18 observation still
  reproduce against b10969 rather than b10434, and did their harness keep one
  response alive across a tool round trip? If it did, the defect is
  harness-shaped rather than model-shaped and the branch notes should say so.
  They are staging the agentic half on pandorum.
- **#15–#17 do not go upstream.** Settled 2026-09-22 by the repository owner,
  who told the fork directly. `fork-only` with `upstream_pr: null` is their
  final state, not a placeholder, so the manifest invariant
  `fork-only ⇔ upstream_pr: null` holds for them permanently and the `note`
  asking for the status to be revisited can go. This also settles the general
  question raised with them: **xollama does not file upstream PRs, and does not
  ask the fork to file them on its behalf** — that decision belongs to the
  repository owner in every case.
