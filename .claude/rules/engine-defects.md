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
  blame every unrelated failure; the signature alone would blame a fixed defect.
- Match on the **bytes**, not the file name — opencoti re-publishes a cut under
  the same name and tag once it is fixed. `describeEngineDefect` checks the
  signature first, calling `fileDigest` only on a hit, so a failure costs nothing.
- Diagnosis only. The `engine-defects` hook is one line in `llm/llama_server.go`:
  `annotateEngineDefect` wraps the original error, never retries or downgrades.
  Retrying is what `XOLLAMA_ENGINE_FALLBACK` is for and it stays opt-in.
- Retire the row in the same commit that moves `llm/engine/pin/index.txt` to an
  artifact without the defect, including a re-cut under the same file names.
  `TestKnownDefectsMatchThePinnedArtifact` fails until you do — but only on the
  release channel: it skips when `pin.Channel` is not `engine.ChannelRelease`,
  because a dev pin carries different bytes and a row is inert there anyway.
- **The table is empty since 2026-10-05.** The c7 r2 row (partial-offload abort
  under the rolling-KV POSITION_WINDOW tactic) was retired when the pin moved
  to opencoti c8 (engine `2610042347001`): the recipe, 70B q3_K_S on a 24 GiB
  card at 32k with `POSITION_WINDOW mode ON (--kv-residency-mode auto)` in the
  log, loads. The result is appended to `docs/evaluations/phase2-engine-ab.md`
  and the `LLAMA_ARG_KV_RESIDENCY_MODE=head` advice left `docs/xollama/slots.mdx`
  in the same commit.
- **Retire on a measurement, never on a changelog.** On 2026-09-20 the c7 row
  was retired on opencoti's word that patch 0253 fixed it; retaking the Phase 2
  overflow axis on the r2 bytes that same day reproduced the abort with
  identical numbers — it covered *two* defects, and only one was fixed.
- **Narrow the accusation as the measurement narrows.** That same row was
  narrowed the same day to the rolling-KV `POSITION_WINDOW` residency tactic
  `--kv-residency-mode auto` picks under real VRAM pressure, not partial offload
  as such; `Workaround` leads with `LLAMA_ARG_KV_RESIDENCY_MODE=head`, measured.
- A row may carry several signatures with different fates: keep the one that
  still reproduces, drop the one that does not, and say so in the comment.
- A retired row lives on as the test fixture — `retiredC7Defect` in
  `llm/engine_defects_test.go` keeps its bytes, signatures and workaround.
- `TestTheShippedTableAccusesExactlyWhatWasMeasured` points the **real** row at
  a fixture digest: the reproducing signature must fire, the fixed one must not.
- Registry row `engine-defects` in `docs/protocols/UPSTREAM-SYNC.md`; user prose
  in `docs/xollama/slots.mdx`, measurements in `docs/evaluations/phase2-engine-ab.md`.
