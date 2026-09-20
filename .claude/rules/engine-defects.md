---
paths:
  - llm/engine_defects.go
  - llm/engine_defects_test.go
---

# Known engine defects

- `knownEngineDefects` in `llm/engine_defects.go` is an accusation against
  specific bytes of a published cut, never a permanent property of the engine.
  Empty is the state to aim for, but **only measurement may empty it**.
- Both halves must match before anything is said: `Signatures` against the
  engine's dying words, `SHA256` against the artifact. The build alone would
  blame it for every unrelated failure; the signature alone would blame a
  defect a newer cut has fixed.
- Match on the **bytes**, not the file name — opencoti re-publishes a cut under
  the same name and tag once it is fixed. `describeEngineDefect` checks the
  signature first and calls `fileDigest` only on a hit, so an ordinary failure
  costs nothing.
- Diagnosis only. The `engine-defects` hook is one line in `llm/llama_server.go`:
  `annotateEngineDefect` wraps the original error, never retries or downgrades.
  Retrying is what `XOLLAMA_ENGINE_FALLBACK` is for and it stays opt-in.
- Retire the row in the same commit that moves `llm/engine/pin.txt` to an
  artifact without the defect, including a re-cut under the same file names.
  `TestKnownDefectsMatchThePinnedArtifact` fails until you do.
- **Retire on a measurement, never on a changelog.** On 2026-09-20 the c7 row
  was retired because opencoti said patch 0253 fixed it and r2 carries that
  patch. Retaking the Phase 2 overflow axis on the r2 bytes that same day
  reproduced the abort with identical numbers: the row covered *two* defects,
  only one was fixed. If a row cannot be re-measured before the pin moves,
  move the pin and keep the row until it can be.
- **Narrow the accusation as the measurement narrows.** That same row was
  narrowed the same day to the rolling-KV `POSITION_WINDOW` residency tactic
  `--kv-residency-mode auto` picks under real VRAM pressure, not partial
  offload as such; `Workaround` now leads with `LLAMA_ARG_KV_RESIDENCY_MODE=head`,
  measured to load the same model on the same card — a workaround that keeps
  the user on this engine beats one that sends them off it.
- A row may carry several signatures, and they can have different fates. Split
  it when the evidence does: keep the signature that still reproduces, drop the
  one that does not, and say in the comment which was measured and how.
- A retired row lives on as the test fixture — `retiredC7Defect` in
  `llm/engine_defects_test.go` keeps its bytes, signatures and workaround — so
  the matcher stays covered even when the shipped table is empty.
- `TestTheShippedTableAccusesExactlyWhatWasMeasured` points the **real** row at
  a fixture digest and asserts both directions: the signature that reproduces
  must fire, the one that was fixed must not. Its predecessor asserted nothing
  came back for an unrelated digest — a test that could not fail. Not again.
- Registry row `engine-defects` in `docs/protocols/UPSTREAM-SYNC.md`. Prose for
  users is in `docs/xollama/slots.mdx`; the measurement behind a row is in
  `docs/evaluations/phase2-engine-ab.md`.
