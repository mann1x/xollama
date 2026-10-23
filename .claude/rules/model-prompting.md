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
- Optional parser interfaces in `model/parsers/parsers.go`: `ThinkingTags()`
  (read via `ThinkingTagsForParser`) and `ToolCallTags()` (`ToolCallTagger`,
  read via `ToolCallStartTagForParser`, implemented by `model/parsers/gemma4.go`
  and `model/parsers/qwen35.go`). The thinking budget is spent across the whole
  response (`reasoning_budget_scope: "response"` in `llm/llama_server.go`), not
  per thinking block; the tool-call open tag rides along as
  `ThinkBudgetResetTag` (`thinkBudgetResetTagForCompletion` in
  `server/routes.go`) so thinking that led to a tool call does not count
  against later thinking.
- `tool_call_progress_v1` (`tool-call-progress` hook) names and touches no
  parser: `server/xollama_tool_call_progress.go` reports, by one rule for every
  model, the raw text of a run of steps of engine output the handler delivered
  nothing for, once the run is longer than `heldBriefly` (8 steps: a parser's
  holds that are no call last 1 to 3, calls 14 or more, measured 2026-10-12)
  (`callProgress.step` and `callProgress.watch` in `ChatHandler`,
  `server/routes.go`), as `Message.ToolCallProgress`
  (`api/xollama_tool_call_progress.go`) on a streamed chat that asks with
  `ChatRequest.ToolCallProgress`; a run's first chunk carries everything held
  so far, and a reported run is closed with `Done` on the chunk that carries
  the tool calls, else `Released`; `Syntax` is the parser's name, or `template`
  for a model without one; off unless asked. Guard:
  `server/xollama_tool_call_progress_test.go`. Prose:
  `docs/xollama/tool-call-progress.mdx`.
- **A Windows path's backslash is not a JSON escape** (`tool-path-backslash`
  hook): `parseTypedToolValue` in `model/parsers/qwen3coder.go` decodes an
  array or object parameter through `keepPathBackslashes`
  (`model/parsers/qwen3coder_paths.go`), so `.\run_game.exe` keeps its `\r`;
  prose and code escapes stay JSON's. Guard:
  `model/parsers/qwen3coder_paths_test.go`.
- Declare tags as file-local consts (e.g. `qwen35ThinkOpenTag` in
  `model/renderers/qwen35.go`). Reuse `renderContentWithImageTags` from
  `model/renderers/image_tags.go` instead of re-implementing image placeholders.
- Every renderer gets a sibling `_test.go` using `github.com/google/go-cmp/cmp`
  plus the helpers in `model/renderers/testhelpers_test.go` (`testArgs`,
  `testPropsMap`, `testArgsOrdered`).
- **A thinking level and a think budget are two settings.** `think`
  (`api.ThinkValue`, `types/model/thinking.go`) is bool or a level, handed to
  the model as written and never a bound; `think_budget` (`api.ThinkBudget`,
  `types/model/think_budget.go`) is a positive token count or a share
  `minimal`..`max` (alias `xhigh`), resolved by `Tokens(window)` and enforced
  by the sampler. A request may carry either, both or neither. Every refusal
  of a value wraps `model.ErrInvalidThinkBudget`, which `handleScheduleError`
  (`server/routes.go`) answers with 400, never 500.
  `ResolveThinking` in `model/renderers/thinking.go` reads only the level.
  Plumbing also spans `thinking/chat_template.go`, `api/types.go` and
  `server/prompt.go` — it is a carried patch, so read
  `docs/protocols/CARRIED-PATCHES.md` before changing its shape.
