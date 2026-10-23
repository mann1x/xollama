---
paths:
  - server/xollama_requires.go
  - server/xollama_requires_test.go
  - server/images.go
  - server/routes.go
  - server/media.go
  - server/media_routes.go
  - internal/mediahub/catalog.go
  - cmd/tweak/mediacmd.go
---

# A `REQUIRES` that names an xOllama release (`requires` hook)

- `server/xollama_requires.go` is additive. The hook is one marked `if` at the
  head of `checkPullRequires` (`server/images.go`), one marked
  `loadRequires(m)` in `scheduleRunnerForModel` and one marked
  `case errors.Is(err, errRequires)` in `handleScheduleError`
  (`server/routes.go`, 400). `server/media.go` and `server/media_routes.go`,
  ours, call it for a media load. Registry row `requires` in
  `docs/protocols/UPSTREAM-SYNC.md`.
- **Why.** Upstream drops the running version's suffix before it compares a
  `REQUIRES`, so that a release candidate can test its release's models. Every
  xOllama version lives in that suffix and a re-release keeps upstream's
  number, so `0.40.3-xollama.2` passed a model stating `0.40.3-xollama.3`.
- `forkRequirement` takes only a requirement whose prerelease names `xollama`
  (`0.40.3-xollama.3`, `0.40.3-xollama`, `0.41.0-rc.1.xollama`).
  `checkForkRequires` compares it against the whole running version with plain
  semver: candidate < release < re-releases by number, and a later upstream
  base beats all.
- **Off means off.** An upstream-form requirement is not handled: it takes
  upstream's path untouched and stays pull-only. A `0.0.0` build passes both,
  as it does upstream.
- **Checked at pull and, for that form only, at load** (`loadRequires`): a
  model that arrived another way (created here, copied in, pulled by a later
  build) is refused by name with `errRequires`, never left to fail inside the
  engine with an error naming a tensor.
- A catalog entry states the first release that serves it:
  `CatalogEntry`'s `Requires` in `internal/mediahub/catalog.go` (`requiresC12`
  for the opencoti c12 entries), stamped on the model by `xollama media`
  through `api.CreateRequest.Requires` (`cmd/tweak/mediacmd.go`). Empty for an
  entry every release with media serves.
- On a conflict in `checkPullRequires` keep upstream's body and re-add the
  `if` first.
- Guards in `server/xollama_requires_test.go`:
  `TestARequirementCanNameAnXollamaRelease`,
  `TestAnUpstreamRequirementKeepsUpstreamsRule`,
  `TestALoadRefusesAModelThatNamesANewerXollama`; upstream's
  `TestCheckPullRequires` passes unchanged.

```sh
go test ./server/ -run 'Requirement|Requires'
```
