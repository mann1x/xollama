# OpenWolf

@.wolf/OPENWOLF.md

This project uses OpenWolf for context management. Read and follow .wolf/OPENWOLF.md every session. Check .wolf/cerebrum.md before generating code. Check .wolf/anatomy.md before reading files.


# CLAUDE.md

See `AGENTS.md` for the shared agent instructions for this repository.

## xollama

A soft fork of ollama. `main` = upstream `v0.34.2` + our changes, with the
full upstream history so merges have a real merge-base.

**Read before changing anything in the upstream tree:**

- `docs/protocols/UPSTREAM-SYNC.md` — what may be edited and how. Every
  change is either an additive file or a marked surgical hook, and hooks go
  in the Registry in the same commit.
- `docs/protocols/CARRIED-PATCHES.md` — the open upstream PRs this fork
  carries. Each is its own `--no-ff` merge and is retired when upstream
  takes it.
- `docs/features/engine-opencoti-llamafile.md` — the engine swap.
- `docs/features/rebrand.md` — what is renamed and what is not.

**Two standing rules that are easy to get wrong:**

1. **Do not rename the Go module path.** It stays `github.com/ollama/ollama`.
   Renaming it costs a ~33% conflict rate on every upstream sync (measured:
   155 of the 474 files upstream touched in `v0.34.0..v0.34.2`) and buys
   nothing. The binary is `xollama`; the import path is upstream's.
2. **Off means off.** With `XOLLAMA_ENGINE=llamacpp` and no xollama flags,
   behaviour must be byte-identical to upstream. That is what keeps an A/B
   against vanilla honest.

Remotes: `origin` = mann1x/xollama, `upstream` = ollama/ollama,
`fork` = mann1x/ollama (where PRs to upstream are staged).
