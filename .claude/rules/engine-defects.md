---
paths:
  - llm/engine_defects.go
  - llm/engine_defects_test.go
---

# Known engine defects

- `knownEngineDefects` in `llm/engine_defects.go` is normally **empty**, and
  empty is the healthy state. A row is an accusation against specific bytes of
  a published cut, never a permanent property of the engine.
- Both halves must match before anything is said: `Signatures` against the
  engine's dying words, `SHA256` against the artifact. The build alone would
  blame it for every unrelated failure; the signature alone would blame a
  defect a newer cut has fixed.
- Match on the **bytes**, not the file name — opencoti re-publishes a cut under
  the same name and tag once it is fixed. `describeEngineDefect` checks the
  signature first and calls `fileDigest` only on a hit, so an ordinary failure
  costs nothing.
- Diagnosis only. The `engine-defects` hook is one line in
  `llm/llama_server.go`: `annotateEngineDefect` wraps the original error rather
  than replacing it, and never retries or downgrades. Retrying is what
  `XOLLAMA_ENGINE_FALLBACK` is for and it stays opt-in.
- Retire the row in the same commit that moves `llm/engine/pin.txt` to an
  artifact without the defect, including a re-cut under the same file names.
  `TestKnownDefectsMatchThePinnedArtifact` fails until you do — but only on the
  release channel: it skips when `pin.Channel` is not `engine.ChannelRelease`,
  because a dev pin carries different bytes and a row is inert there anyway.
- A retired row lives on as the test fixture — `retiredC7Defect` in
  `llm/engine_defects_test.go` keeps its bytes, signatures and workaround — so
  the matcher stays covered while the shipped table is empty.
- `TestTheShippedTableAccusesNothing` pins that empty state: the signatures
  that used to fire must not fire against the artifact `main` pins today.
- Registry row `engine-defects` in `docs/protocols/UPSTREAM-SYNC.md`. Prose for
  users is in `docs/xollama/slots.mdx`; the measurement behind a row is in
  `docs/evaluations/phase2-engine-ab.md`.
