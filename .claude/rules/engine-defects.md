---
paths:
  - llm/engine_defects.go
  - llm/engine_defects_test.go
---

# Known engine defects

- `knownEngineDefects` in `llm/engine_defects.go` names bugs in the engine build
  we ship and cannot patch. A row speaks only when both halves match: a `SHA256`
  of the artifact that actually failed and one of its `Signatures` in the
  engine's dying output. It is diagnosis, never recovery — nothing retries,
  downgrades, or changes what was launched.
- The table is a **release-channel invariant**. A row is retired the day a
  release pin names an artifact without the defect, never because a `dev` pin
  points at different bytes — `TestKnownDefectsMatchThePinnedArtifact` in
  `llm/engine_defects_test.go` skips unless `pin.Channel` is
  `engine.ChannelRelease` (`llm/engine/pin.go`), and a row is inert on a dev
  build because its `SHA256` cannot match.
- Match bytes, never a file name or the `tag`: opencoti re-publishes a cut in
  place, so a name-matched row keeps blaming a build that is already fixed.
- Pin mechanics and the channel table: `.claude/rules/engine-pin.md` and
  `docs/features/engine-opencoti-llamafile.md`.
