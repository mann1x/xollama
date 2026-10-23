---
paths:
  - api/xollama_notice.go
  - llm/engine_notice.go
  - llm/engine_notice_test.go
  - llm/engine_notice_templates_test.go
  - llm/engine_notice_v3_test.go
  - llm/engine/capability.go
  - llm/engine/notices.go
  - types/xollama/notices.go
  - types/xollama/notices_default.json
  - server/notice.go
  - server/notice_test.go
  - server/xollama_notices.go
  - server/xollama_notices_test.go
  - server/identity.go
  - cmd/tweak/notices.go
  - docs/xollama/notices.mdx
---

# Generation notices (`notices` hook)

- opencoti contract v2 (`generation_notice_v2`, c13 r3; v1 was never pinned):
  a client opts in per request with a top-level `notices` object
  (`ChatRequest.Notices` in `api/types.go`); xollama passes it to the engine
  **unchanged**, never edits or validates it. The engine's 400 reaches the
  client as a 400.
- **xOllama decides the templates** (owner, 2026-10-15). A model's templates
  are a layer of their own, `xollama.MediaTypeNotices`, in the engine's
  `--notice-templates` JSON shape; the launch passes its blob path
  (`appendNoticeTemplatesArgs`, `llm/engine_notice.go`), loaded directly, never
  copied. A model without one passes **no flag**: the engine's built-in ladder
  is `xollama.DefaultNoticeTemplates` (`notices_default.json`), equal byte for
  byte on r3 (`--print-notice-config` with no file); re-check at every pin
  move. Nothing is ever written for the default. `LlamaServerConfig` is
  compared on every request (`needsReload`): never put a computed or
  written path in it (bug-285). Never ask the engine at load what was
  planted: we know it.
- **A model's file is completed with the defaults** (`CompleteNoticeTemplates`)
  before it is stored. The engine fills a missing phase or message from its own
  built-in ladder, whose openings the model's hide spans would not list: found
  in the 2026-10-16 e2e with an answer-only file.
- **The engine judges a file, at the save.** `applyNoticeTemplates`
  (`server/xollama_notices.go`, after `applyDraftHead` in `CreateHandler`) runs
  `--server --print-notice-config --notice-templates <blob>`
  (`engine.PrintNoticeConfig`) and refuses with the engine's last stderr line.
  A layer edited by hand afterwards is refused by the engine at boot, and that
  line reaches the client through the load error. `notices.templates` in the
  create request is a digest or `none`; it is never stored in `xollama.json`.
- **Off means off.** `noticesFor` returns the object only when the running
  engine's `/props` lists the feature; `--notice-templates` is passed only when
  `engine.NoticesServed` says the engine takes it (the pin, or for an
  operator's `XOLLAMA_ENGINE_PATH` one cached `--print-notice-config` run: an
  engine that does not know the flag refuses the whole command line).
  `XOLLAMA_ENGINE=llamacpp` and stock llama.cpp get upstream's body and argv.
- **The planted notice stays in the content** (owner, 2026-10-14); the client
  hides it by the `hide` spans (`{open, close|null}`, `NoticeTemplates.Hide`,
  the engine's own rule). Never strip it here.
- `/api/xollama` lists `generation_notice_v2` (`servedFeatures`) and carries
  the default `GenerationNotice` block only while `noticesServed()`;
  `/api/show` carries the model's own (`modelGenerationNotice`). The done
  chunk carries `notices_planted` (`Metrics.NoticesPlanted`, from the engine's
  `timings.notices_planted`).
- `POST /api/xollama/notice` (`NoticeHandler`) routes a waiting call to the
  runner holding the request with that key (`trackNotice`, `PostNotice`); the
  answer is the engine's own (200, 400, 404 no such key, 409 shared key).
- **Testing**: a notice is planted only at a prose line start. A prompt whose
  text past the step is a list, code or one long paragraph plants nothing,
  which is the rule, not a defect. Use short prose paragraphs. The e2e script
  of 2026-10-16 is `/srv/ml/xo26/e2e.py` (side server on 22498).
- **Which models follow them** (opencoti, c14 42.1, 2026-10-16): Gemma 4
  12B / 26B-A4B / 31B do not follow answer-budget notices in any form tried
  (0-3 of 24 finished inside the cap, the same as no notice); thinking
  notices at best a weak lift. Gemma 4 GGUFs ship `<unusedNN>` as NORMAL
  tokens: before r4 (bug-4108) the bar let them through and
  `<engine_<unused27> ...>` passed the hide span (measured through xOllama,
  98e-v7-coder Q4_K_M, 48 sampled answers: r3 4 leaked, r4 0; `/srv/ml/c13r4/gemma-ab.py`).
  A test needs answers past 60 % of the phase budget with >= 200 tokens
  left: num_predict 900 and "at least thirty paragraphs" plants, 1400 with
  short prose plants nothing.
- Registry row `notices` in `docs/protocols/UPSTREAM-SYNC.md` lists every hook.
- **Contract v3** (opencoti c13 r5, mails #1576-#1582; the owner gave the word for r5 and for `.4` on 2026-10-17). It is opt-in per request with `"version": 3`, and xOllama passes it through unchanged. The engine puts a top-level `notice` `{index, phase, channel, text, break[, cut]}` on the chunk of the line's first token, on both `/completion` and chat, and `timings.notices` on the final chunk. `noticeChunk` (`server/notice.go`) sends it as a chunk of its own BEFORE the chunk it came on: a parser can hold that text back or drop the chunk, but never sends it earlier, so the client's rule ("the next occurrence of text+break") holds. The done chunk carries `notices` (`api.Metrics`); a non-streamed answer has only the list. A chunk carrying only a notice must not be dropped by the stream loops' "empty chunk" guards (both carry a hook). `generation_notice_v3` is advertised only beside v2 (`noticesV3Served`). Guards: `llm/engine_notice_v3_test.go`; `TestAV3NoticeReachesTheClientBeforeItsLine` and `TestNoticesV3IsAdvertisedOnlyBesideV2` in `server/notice_test.go`. The first was verified by breaking the hook.

```sh
go test ./server/ ./llm/ ./types/xollama/ -run Notice
```
- **v3 `copy_bar`** (patch 0693, mail #1589) is a field inside `notices` that the engine reads; xOllama never inspects it. Only the feature `generation_notice_copy_bar` is ours to advertise, and only alongside v3 (`noticesCopyBarServed`; for an operator's engine path, the printed block's `v3.copy_bar`, `printedV3`). Guards: `TestTheCopyBarIsAdvertisedOnlyWithV3` and `llm/engine/notices_v3_test.go`.
