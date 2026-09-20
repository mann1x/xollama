---
paths:
  - llm/engine/**
  - cmake/opencoti-fetch.cmake
---

# opencoti engine pin

- `llm/engine/pin.txt` is the only place that names the engine artifact, and it
  is parsed twice — by `ParsePin` in `llm/engine/pin.go` and by
  `cmake/opencoti-fetch.cmake`. A new directive must be readable by both;
  `TestPinFormatIsWhatCMakeParses` in `llm/engine/pin_test.go` holds them to it.
- `rev` is a 40-character commit sha, never a branch or tag: opencoti re-cuts a
  release in place, so a moving rev fetches bytes the pinned `sha256` rows
  reject. `ParsePin` rejects anything shorter or non-hex.
- `channel` is required and is `release` or `dev` (`ChannelRelease` /
  `ChannelDev` in `llm/engine/pin.go`). It is declared, never guessed from the
  repo name; `cmake/opencoti-fetch.cmake` names a dev channel in the build log.
- Never assert which Hugging Face repo is pinned — the dev channel points at a
  different repo whenever a cut is in flight. Tests check the `<owner>/<name>`
  shape only.
- Engine capabilities come from `feature` rows, read through `pin.HasFeature`
  (`featureSWACacheTypes` in `llm/engine/capability.go`), and are never inferred
  from the cut number in `tag`: a dev build carries part of the next cut under
  the previous cut's tag.
- `accel <arch> <backend>` rows declare what the pinned BYTES accelerate, which
  is a different fact from the tested matrix in `llm/engine/policy.go`. Routing
  is the two intersected — `pinUncovered` / `pinUncoveredIn` call
  `pin.Accelerates`, so an uncovered accelerator goes to llama.cpp instead of
  being served silently on the CPU. Backend spellings must be in `knownBackends`
  (`llm/engine/policy.go`); a typo is a parse error.
- A tested platform does **not** have to have an artifact — a dev pin ships a
  subset. It must be served by the pin or refused for a stated reason, which is
  what `TestEveryTestedPlatformIsServedOrRefused` in `llm/engine/pin_test.go`
  asserts: no `bin` row for a tested platform means `pinUncoveredIn` has to
  return a reason, never route.
- Asset rows are `bin` (the engine) or `dso` (a side-loadable GPU payload staged
  beside the binary); release bins embed their payloads, a dev snapshot is a bare
  APE that needs one. Address them with `pin.Asset` (bin rows only) and `pin.DSO`;
  build offline with `-DLOCAL_DSO_FILE=<payload>`. One row per kind per arch and
  at least one `bin` — `TestCommittedPinParses` fails duplicates and dso-only pins.
- A test about policy must not also be a test of what this branch pins: stub the
  pin with `withPin` (`llm/engine/coverage_test.go`), which swaps `loadPin` and
  restores it in `t.Cleanup`. `TestSupportsDeviceHonoursTheCUDAComputeFloor` in
  `llm/engine/policy_test.go` routes against an all-payload pin so what it
  measures is the compute floor, not the payloads the shipped pin happens to carry.
- Moving to a new artifact is one commit: `repo`, `rev`, `tag`, `channel`, every
  `sha256`, and the `feature` and `accel` rows corrected to what the new bytes
  carry.
- Background and the channel table: `docs/features/engine-opencoti-llamafile.md`.
