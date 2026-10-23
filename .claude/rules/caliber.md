# Caliber and CLAUDE.md

Caliber (`caliber refresh`, provider `claude-cli`) keeps the agent configs in
sync and, usefully, **version-syncs** CLAUDE.md and README (it corrected the
status line and README from v0.40.3/v0.40.6 to v0.40.10/c13 on 2026-10-23). But
its CLAUDE.md edit is **LLM-driven and not deterministic**, and it has
regressed the file:

- **2026-10-23 (`xo-37`):** a refresh run in a `sync/*` worktree silently
  **dropped ten documented hook sections** from CLAUDE.md — `notices`,
  `tool-choice`, `system-telemetry`, `opencoti-first`, `backend-copies`,
  `drafter_state_v1`, `systemone`/`decisionEngine`, `windows-mlx`,
  `think_budget`, `generation_notice_v3`. The regressed file was committed to
  `origin/dev`; the complete one survived only as uncommitted state in the main
  worktree. It was reconciled back to the union by hand.

## Rules

- **Caliber does not back up before a refresh.** `.caliber/backups/` holds only
  the install-time snapshot, not a per-run one. A dropped section is therefore
  **not auto-recoverable** — you must catch it before the commit.
- **After any `caliber refresh` (or any commit whose hook ran it), diff
  CLAUDE.md before pushing.** A refresh may only **add** or **version-sync**; it
  must never remove a section documenting a `// xollama-hook:` feature. If a
  section is gone, restore it (`git show <good-rev>:CLAUDE.md`, or the prior
  commit) and re-commit. Treat a caliber run that drops a documented hook as a
  bug, not a style change.
- **Never patch caliber for xollama alone.** It is a shared, root-owned tool
  (`/srv/dev-disk-by-label-opt/dev/ai-setup`) used by other projects; a change
  there affects all of them.
- **CLAUDE.md carries no `<!-- caliber:managed:* -->` markers**, so caliber
  treats the whole file as refreshable — which is why its LLM can over-trim the
  hand-authored architecture. Scoping caliber to only its own footer blocks
  (`Before Committing`, `Session Learnings`, `Model Configuration`,
  `Context Sync`) by wrapping them in the correct markers is a
  **caliber-maintainer** change; it is not done ad hoc here, because a wrong
  marker is worse than none and the exact format/behaviour is unverified.
- **Keep the main worktree current** (`docs/protocols/UPSTREAM-SYNC.md`,
  `RELEASE.md` step 9). The drift that exposed this was a stale main-worktree
  `dev` 248 commits behind `origin/dev`; a current worktree means caliber always
  refreshes the up-to-date file, and there is no second, divergent copy to lose.
