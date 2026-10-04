---
paths:
  - llm/engine/payload.go
  - llm/engine/payload_test.go
  - llm/engine/opencoti.go
  - llm/engine/artifact_of_test.go
  - llm/llama_server.go
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
- Identity is **content, never the path**: size + mtime first (against every
  stat already seen for the owning bytes, `Seen`, at most 8), sha256 only when
  none matches. The steady state costs one stat, hashing 700 MB happens once
  per new copy, and the same bytes at two paths neither purge nor re-hash
  (`TestTheSameBytesAtTwoPathsShareOnePayload`). Each half has exactly one test
  that only it can satisfy (`TestStatIsTrustedInTheSteadyState`,
  `TestTouchedArtifactWithSameBytesKeepsItsPayload`); keep it that way, or
  deleting one half will leave every test passing.
- **Preparation is locked**: `payloadMu` within the process and an exclusive
  file lock on `<root>/.xollama-payload.lock` across processes
  (`payload_lock_unix.go` flock, `payload_lock_windows.go` LockFileEx), held
  from reading the marker to writing it. Without it an LLM engine and a media
  engine starting together both read "no marker" and one purges the tree the
  other is unpacking into. Guard: `TestConcurrentLaunchesNeverPurgeEachOther`
  (fails with both locks removed; passes with either).
- Every file and directory here is created through `internal/fsowner`
  (`MkdirAll`, `CreateTemp`, `OpenFile`, `WriteFile`), never `os` directly.
- Since opencoti 0330 a bundled payload extracts to a **content-keyed**
  `~/.llamafile/v/<ver>-<tag>[-dev]/p/<crc32>-<size>/`, and the split form
  extracts nothing, so the re-cut collision above no longer reaches the
  kernels on any pin from 0330 on. The purge now only bounds the directory
  at one payload; the private HOME stays (owner, 2026-09-21: xollama works
  in its own environment).
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
