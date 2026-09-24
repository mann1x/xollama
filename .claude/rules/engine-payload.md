---
paths:
  - llm/engine/payload.go
  - llm/engine/payload_test.go
  - internal/fsowner/**
---

# The engine's GPU payload directory

- A **self-extracting** engine unpacks its `ggml-*.so` into
  `$HOME/.llamafile/v/<engine-version>/`. Of those three segments only `$HOME`
  is reachable from outside: `.llamafile` is llamafile's `g_app_name` (in-process
  C call) and the version is compile-time. That is the whole reason this file
  exists.
- **Two builds under one tag share the directory.** opencoti re-cuts a release
  in place, so c7 r1 and r2 both resolve to `opencoti-0.10.5-c7`. The engine
  only unpacks when what is there is *older*, then dlopens what it found — so a
  host that ran r1 can keep running r1's kernels under an r2 binary. opencoti
  hit the coarser form (their bug-2272, c5/c6/c7 all in `v/0.10.3/`) and
  namespaced by cut; that cannot separate re-cuts *of* a cut.
- `PreparePayloadHome` gives the subprocess a private `HOME` holding **exactly
  one** payload, purging whatever a different artifact left. The user's
  `~/.llamafile` is never read or written — they may run any opencoti build by
  hand without interacting with ours.
- **The payload belongs with the rest of the runtime**, so the preferred root is
  `<ml.LibOllamaPath>/engines/payload` — the directory already holding
  `llama-server` and the ggml backends, which is `<install>/lib/ollama` on all
  three platforms (`%LOCALAPPDATA%\Programs\Ollama\lib\ollama` on Windows,
  `/usr/local/lib/ollama` on Linux, `Contents/Resources/lib/ollama` on macOS).
  Never the home directory of whoever started the server — on a Linux service
  that is `root`.
- **It is a list, not a choice** (`DefaultPayloadRoots`). The preferred root is
  unwritable on two of the three platforms as installed: a Linux package leaves
  it `root`-owned while the service runs as `ollama`, and macOS puts it inside a
  signed bundle. `~/.ollama/engines/payload` follows as the fallback.
- **Writability is probed before any work** (`ensureWritable`). The Linux case
  is a directory that already *exists*, so `MkdirAll` succeeds and only the
  first write fails — which without the probe is after hashing 678 MB, on every
  model load, forever. `ensureWritable` and `digestOf` are both vars **because
  the tests run as root**, and root ignores the mode bits that would otherwise
  express an unwritable directory; see
  `TestARootWeCanCreateButNotWriteIsSkippedBeforeAnyWork`.
- **Only on the opencoti path.** Stock `llama-server` extracts nothing and is
  launched with the environment it inherited; the `engine-payload` hook in
  `llm/llama_server.go` is inside `if usedOpencoti`.
- **Hand it the artifact, not the program.** Off Windows the APE runs through
  `sh`, so the hook passes `engine.ArtifactOf(name, args)` (`llm/engine/opencoti.go`),
  never `name` — which once hashed `/usr/bin/sh` on every Linux load.
  `TestTheArtifactOfALaunchIsTheEngineNotTheShell` holds it.
- **Never fatal.** An unusable root logs a warning and returns `""`, which keeps
  the inherited `HOME` — the behaviour before this existed. Turning a directory
  we cannot write into a model that will not load would be a worse trade.
- **Purging is scoped to `payloadDirName` (`.llamafile`)**, never the root
  itself, so a misconfigured root cannot delete a user's files.
  `TestPurgeIsScopedToWhatWeCreate` holds it there.
- Identity is **path + size + mtime first, sha256 only when those disagree** —
  the steady state costs one stat, and hashing 700 MB happens on the launch
  after the artifact changed. Each half has exactly one test that only it can
  satisfy (`TestStatIsTrustedInTheSteadyState`, `TestTouchedArtifactWithSameBytesKeepsItsPayload`);
  keep it that way, or deleting one half will leave every test passing.
- A **split** artifact (bare APE + side-loaded `ggml-cuda.so`) extracts nothing
  at all — verified by running one with an empty `HOME`: it works and creates no
  cache. Nothing here applies to it, and it is the shape to prefer when opencoti
  publishes it for a release channel.
- **Root must not create it as root.** On a packaged Linux install the service
  runs as `ollama`; a one-off root command that leaves a `root:root` payload
  directory locks the service out of its own purge. `ensureWritable` calls
  `fsowner.Intended(envconfig.Models())` and adopts the root when it answers.
  The model store is the evidence — whoever owns it must be able to write it,
  so that identity is the service. A root-owned store is deliberately NOT
  evidence: that is the state the bug leaves behind.
- Directories get **setgid + group write**, not just a chown, because a purge
  removes files by writing the *directory*. That is what lets the service clear
  a payload an earlier root run unpacked. Use `os.ModeSetgid`, never a raw
  `0o2000` bit — `os.Chmod` takes an `os.FileMode` and silently drops it.
- Ownership is never fatal (`AdoptQuietly`): it is a correctness measure for the
  NEXT process, not a reason to refuse this one.
