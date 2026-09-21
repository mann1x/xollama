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
A patch is never authored anywhere else. The one branch that never got the
`up-` prefix, `gemma4-toolcall-in-thinking`, is the one that went astray.

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
  "integration": { "branch": "think-budget", "sha": "645ec440" },
  "patches": [
    { "slug": "gemma4-toolcall-in-thinking",
      "branch": "up-gemma4-toolcall-in-thinking",
      "sha": "49be8af6",
      "upstream_pr": 18307,
      "rebased_onto": "v0.34.2",
      "files": ["model/parsers/gemma4.go", "model/parsers/gemma4_test.go"],
      "status": "open-upstream" }
  ]
}
```

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

**R7 — `mann1x/ollama@main` is a plain mirror of upstream.** It was six weeks
stale, which is what made every fork PR a 451-file diff burying the two files
that mattered. Mirrored, it is never a development target and never a base
anyone reasons about.

> **xollama fetches nothing from `mann1x/ollama@main`.** Verified 2026-09-21:
> no build file, cmake module, pin, workflow or Go source references
> `mann1x/ollama` at all. We touch the `fork` remote only to fetch `up-*`
> branches for merges. Mirroring `main` costs this repo nothing.

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
`upstream_pr`. The fork-internal PRs on `mann1x/ollama` (#1, #3–#7) duplicate
the upstream ones and are not a review surface for this repo; they may be
closed.

## Open items

- First `PATCHES.json` not yet published. On receipt, xollama merges
  `up-gemma4-stray-channel-name` (upstream #17626, listed in
  `CARRIED-PATCHES.md` but never actually merged) and re-merges the
  think-budget set at its current sha to pick up `ToolCallTags()`.
