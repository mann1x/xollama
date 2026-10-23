---
paths:
  - api/xollama_tool_choice.go
  - api/xollama_tool_choice_test.go
  - api/types.go
  - llm/engine_tool_choice.go
  - llm/engine_tool_choice_test.go
  - llm/server.go
  - llm/llama_server.go
  - server/xollama_tool_choice.go
  - server/xollama_tool_choice_test.go
  - server/routes.go
  - server/identity.go
  - docs/xollama/tool-choice.mdx
---

# `tool_choice` none (`tool-choice` hook, `tool_choice_none_v1`)

- A request that says `tool_choice` none keeps its tools in the prompt, so the
  client's prefix is reused, and the model must not call them (cerebriline,
  mail #1733; owner 2026-10-22). On pandorum, a side call that only told the
  model not to use them wrote an editor call until it hit its 4000-token cap.
- `api.ToolChoiceNone` (`api/xollama_tool_choice.go`) reads `ChatRequest.ToolChoice`:
  the string `"none"` (OpenAI) or `{"type":"none"}` (Anthropic). Anything else,
  absent included, leaves the model free to call.
- **Completion path.** `noToolCallTag` (`server/xollama_tool_choice.go`) takes
  the built-in parser's `ToolCallStartTagForParser`, else the template parser's
  `Tag()`, and passes it as `CompletionRequest.NoToolCallTag`. `banToolCall`
  (`llm/engine_tool_choice.go`) tokenizes the tag with the engine's own
  `/tokenize` (`parse_special: true`, `add_special: false`):
  - one token: `logit_bias [[id,false]]`;
  - several tokens: a stop string, said at Warn;
  - no letter in the tag (`{`): nothing, because banning it would ban every brace.
- **Chat path.** `chatToolChoice` sends `tool_choice: "none"` in
  `llamaServerChatRequest`'s body, and llama-server honours it.
- **Belt and braces.** `forbiddenToolCalls` runs after each
  `completeToolCalls` in `ChatHandler` and drops any call written anyway, at
  Warn. The client never receives a call it forbade.
- **Off means off.** Every field (`NoToolCallTag`, `ToolChoiceNone`,
  `LogitBias`, the body's `tool_choice`) stays empty unless the request says
  none and carries tools, so every other body is upstream's. Neither engine
  needs a change: both take `logit_bias` and `stop`.
- `api.FeatureToolChoiceNone` is listed in `xollamaFeatures`
  (`server/identity.go`). Registry row `tool-choice` in
  `docs/protocols/UPSTREAM-SYNC.md`. User docs: `docs/xollama/tool-choice.mdx`.

```sh
go test ./api/ ./llm/ ./server/ -run 'ToolChoice|ToolCall'
```
