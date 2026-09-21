# Ownership of everything xollama writes

- **Never call `os.MkdirAll` / `os.WriteFile` / `os.Create` / `os.CreateTemp` /
  `os.OpenFile` for a path under the model store or `~/.ollama`.** Use the
  `internal/fsowner` wrapper of the same name. xollama is a fork people try
  beside an ollama they depend on; a root-run command that leaves root-owned
  files there breaks *their* installation, and it surfaces as a slow model list
  rather than as an error (measured once: 9.89 s vs 0.06 s).
- The decision comes from the **destination**, not the call site: the owner of
  the nearest existing ancestor directory. So the wrappers are safe anywhere,
  and outside a service-owned tree they are `os` plus one stat.
- **A root-owned ancestor is not evidence** — that is the state the bug leaves
  behind, and `<install>/lib/ollama` is root-owned on every packaged install.
  Fall back to the named service account.
- **Read the owner before creating.** `MkdirAll` must resolve the owner from the
  shallowest *missing* path, because once `os.MkdirAll` has run, the nearest
  existing ancestor is a directory this call just made as root. That bug was
  caught by `TestMkdirAllHandsOverEveryLevelItCreates`, which saw uid 990 (the
  real `ollama`, via the named-account fallback) instead of the store's owner.
- **Directories get setgid + group write**, not just a chown: a purge removes
  files by writing the *directory*. Use `os.ModeSetgid`, never a raw `0o2000`
  bit — `os.Chmod` takes an `os.FileMode` and silently drops it.
- **Adoption is never fatal.** It is a correctness measure for the next process,
  not a reason to refuse this one.
- The mirror case — an unprivileged user against a service-owned store — cannot
  be chowned. `fsowner.Preflight` warns once from `Serve`, naming owner, current
  account, consequence and fix. Do not turn it into a refusal.
- Registry row `store-ownership`; prose in `docs/features/store-ownership.md`.
- Verify changes here against a real root-run `pull` into an `ollama`-owned
  store, not only unit tests: with adoption 0 files are foreign, without it 11
  are and the service cannot write `blobs/`.
