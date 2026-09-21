---
paths:
  - llm/engine/payload.go
  - llm/engine/payload_test.go
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
- `PreparePayloadHome` gives the subprocess a private `HOME` under ollama's own
  directory holding **exactly one** payload, purging whatever a different
  artifact left. The user's `~/.llamafile` is never read or written — they may
  run any opencoti build by hand without interacting with ours.
- **Only on the opencoti path.** Stock `llama-server` extracts nothing and is
  launched with the environment it inherited; the `engine-payload` hook in
  `llm/llama_server.go` is inside `if usedOpencoti`.
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
