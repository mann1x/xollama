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

## Open items

- **First manifest published 2026-09-21** (`base: v0.34.2`,
  `integration.sha: 3af5f361`, release `v0.34.2-1-thinkbudget` @ `645ec440`),
  carrying twelve patches. Every branch was rebased onto `v0.34.2` and
  force-pushed, and all twelve upstream PRs were confirmed open on their new
  heads after the push. xollama's merges predate that rewrite, so consuming the
  manifest is a re-merge of rewritten history, not a fast-forward.
- **Consumed 2026-09-21**: all twelve re-merged at the manifest shas, in
  `patches[]` order, each its own `--no-ff` merge; shas in
  `CARRIED-PATCHES.md`. Six conflicted, none through ordering.
- **`ToolCallTags()` has no home.** It was the reason for re-merging #17566,
  and it did not arrive. It exists on `think-budget` and `thinkbudget-0.34.2`
  (`model/parsers/parsers.go`, `gemma4.go`, `qwen35.go`, both test files) and on
  **no** `up-*` branch at any manifest sha — a patch authored on the integration
  branch, which R2 forbids. R4 says do not hand-copy it, so it stays out of this
  tree until it has a branch and a manifest entry. The protocol found this; a
  survey of branch names never would have.
- Three patches needed repair to land on `v0.34.2` and the fixes live on the
  branches, not here — `up-jinja-runner-reuse` (struct no longer comparable
  with `!=` after `DraftModelShardPaths` was added; two upstream `runnerRef`
  fixtures had to set the new field), `up-mlx-libdl` (0.34 moved
  `x/mlxrunner/`; relocated to `mlx/mlx.go`, and the upstream PR title still
  says the old path), `up-think-budget` (`go vet` only: two test files still
  named `fs/ggml`, which 0.34.2 moved to `internal/testutil/gguf` — invisible
  to `go build ./...`).
- **Both rule corrections of 2026-09-21 were found by executing the rules, not
  by reading them** — R1's cross-fork rename carve-out and R7's workflow
  exception. Neither announced itself: the rename closed a PR and the mirror
  deleted a workflow, and both were caught only because someone checked.
