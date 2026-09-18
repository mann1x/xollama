---
paths:
  - model/renderers/**
  - model/parsers/**
  - thinking/**
  - harmony/**
  - template/**
---

# Renderers, parsers and thinking

- A renderer implements `Render(messages, tools, think) (string, error)` and
  `LeadingBOS() string` (interface in `model/renderers/renderer.go`). Wire the
  name into `rendererForName`, or call `renderers.Register` for out-of-tree ones.
- Output parsing lives in `model/parsers/parsers.go`. Renderer and parser names
  are resolved from the `Renderer` / `Parser` fields in `types/model/config.go`.
- Declare tags as file-local consts (e.g. `qwen35ThinkOpenTag` in
  `model/renderers/qwen35.go`). Reuse `renderContentWithImageTags` from
  `model/renderers/image_tags.go` instead of re-implementing image placeholders.
- Every renderer gets a sibling `_test.go` using `github.com/google/go-cmp/cmp`
  plus the helpers in `model/renderers/testhelpers_test.go` (`testArgs`,
  `testPropsMap`, `testArgsOrdered`).
- Thinking-budget plumbing spans `thinking/chat_template.go`, `api/types.go`
  (`api.ThinkValue`) and `server/prompt.go` — it is a carried patch, so read
  `docs/protocols/CARRIED-PATCHES.md` before changing its shape.
