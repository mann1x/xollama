---
name: add-model-renderer
description: Adds a chat-template renderer and its matching streaming output parser to the xollama fork, following the exact repo pattern: tag consts at file top, <Model>Renderer with Render/LeadingBOS in model/renderers/<model>.go, <Model>Parser with Init/Add/PreservedTokens/ThinkingTags in model/parsers/<model>.go, registration in rendererForName and ParserForName, plus table tests using testhelpers_test.go comparers and a testdata jinja fixture. Use when the user says 'add support for <model> prompt format', 'new renderer', 'new parser', 'tool calling for <model>', 'render the <model> chat template', or edits a file under model/renderers/ or model/parsers/. Do NOT use for Modelfile Go templates in template/, for the generic tools parser in tools/, for harmony/, or for MLX architecture ports under mlxrunner/model/.
paths:
  - model/renderers/**
  - model/parsers/**
  - server/renderer_resolution.go
  - server/prompt.go
---
# Add a model renderer + parser

A renderer turns `[]api.Message` into the model's exact prompt string. A parser turns the model's streamed output back into content / thinking / tool calls. They are selected by name from `m.Config.Renderer` and `m.Config.Parser` in `server/prompt.go` and `server/images.go`. Both names are plain strings — a typo silently disables the feature or errors at chat time.

## Critical

1. **Ground truth is the published chat template, never memory.** Read the `chat_template` field from the checkpoint's tokenizer_config.json, or its standalone chat-template jinja file (the same order `create/metadata.go` `readChatTemplateStrict` uses). Copy it byte-for-byte into a fixture under `model/renderers/testdata/` and record the publisher revision + SHA-256 in a comment above the const, exactly as `model/renderers/glimmer_reference_test.go` does. Do not invent tag spellings, whitespace, or newlines — the tests compare full strings.
2. **Never rename the Go module path.** Imports stay on upstream's module path, as declared in `go.mod`. The binary is xollama; the import path is upstream's.
3. **`model/renderers/` and `model/parsers/` are the upstream tree.** A new per-model file plus its sibling test file are additive and always allowed. Adding one `case` to `rendererForName` in `model/renderers/renderer.go` and to `ParserForName` in `model/parsers/parsers.go` is the upstream-shaped way and is acceptable for a renderer you intend to send upstream. For a **fork-only** model, prefer the registry instead — `func init() { Register("<name>", func() Renderer { return &XRenderer{} }) }` in your additive file (see `model/renderers/renderer_test.go` and `parsers.Register`) — it leaves upstream files untouched and costs zero merge conflicts. Read `docs/protocols/UPSTREAM-SYNC.md` before editing either switch.
4. **Off means off.** Only models whose `Config.Renderer` / `Config.Parser` names your new strings may change behaviour. Never touch an existing case, an existing default, or `resolveRendererName` mappings while adding a new model.
5. **Tests land in the same commit as the switch case.** No renderer merges without a table test asserting the full prompt string; no parser merges without a test that splits tags across chunk boundaries.

## Instructions

### Step 1 — Capture ground truth

```sh
python3 -c "import json;print(json.load(open('<modeldir>/tokenizer_config.json'))['chat_template'])" > model/renderers/testdata/foo1_chat_template.jinja
sha256sum model/renderers/testdata/foo1_chat_template.jinja
```

List every literal control token in the template (BOS, role headers, thinking open/close, tool-call open/close, tool-result, end-of-turn) and the generation-prompt suffix the template emits when `add_generation_prompt=true`.

**Verify before Step 2:** the fixture file exists under `model/renderers/testdata/`, the `sha256sum` output is recorded for the test comment, and you can state the exact string the prompt ends with for (a) thinking on, (b) thinking off. If the template has no thinking block, say so explicitly — it decides `HasThinkingSupport()` later.

### Step 2 — Write the renderer file in `model/renderers/`

Uses the tag inventory from Step 1. File layout, in this order:

```go
package renderers

import (
	"strings"

	"github.com/ollama/ollama/api"
)

// FooRenderer renders the Foo-1 chat template: <one paragraph naming the
// model family and the structure of a turn>.
const (
	fooBOS         = "<|begin_of_text|>"
	fooStartTurn   = "<|start|>"
	fooMessage     = "<|message|>"
	fooEndTurn     = "<|eot|>"
	fooStartThink  = "<|think|>"
	fooEndThink    = "<|/think|>"
)

type FooRenderer struct {
	useImgTags bool
	isThinking bool
}

func (r *FooRenderer) LeadingBOS() string { return fooBOS }

func (r *FooRenderer) Render(messages []api.Message, tools []api.Tool, think *api.ThinkValue) (string, error) {
	var sb strings.Builder
	sb.WriteString(fooBOS)
	// ... one loop over messages, switch on msg.Role: system / user / assistant / tool
	return sb.String(), nil
}
```

Rules taken from the existing renderers:

- **Const names are prefixed with the model slug** (`glimmerBOS`, `cohereEndThinking`, `lfm2ToolListStartTag`) — the package is flat, so unprefixed names collide.
- **Variant fields, not variant types.** One struct with fields is the pattern: `LFM2Renderer{IsThinking: true}`, `Gemma4Renderer{useImgTags: ..., emptyBlockOnNothink: true}`, `DeepSeek3Renderer{Variant: Deepseek31}`. Export a field only if another package constructs it; otherwise keep it unexported like `useImgTags`.
- **`LeadingBOS()` must be byte-identical to the prefix `Render` emits, or `""`.** `completionPrompt` in `llm/llama_server.go` trims exactly that prefix when the GGUF tokenizer adds BOS itself. Return `""` when the template has no BOS (`GLM47Renderer`). If the reference prompt shows BOS as a *token* added at encode time rather than text (Cohere), still return the literal from `LeadingBOS()` but do **not** write it in `Render`.
- **JSON must match the template's filter.** Jinja `tojson` emits `", "` / `": "` separators — use `marshalWithSpaces` from `model/renderers/json.go`, not `json.Marshal`.
- **Images:** when the struct has `useImgTags`, route user content through `renderContentWithImageTags` (`model/renderers/image_tags.go`) so `[img-N]` placeholders are produced for the runner; the false branch emits the model's own patch token. The flag is fed by the global `RenderImgTags` at construction time in the switch.
- **Tool properties are ordered.** Iterate `api.ToolPropertiesMap` through its ordered API; never range a plain Go map into the prompt.
- **`think` semantics:** `think == nil` means the model default (spell the default out in a comment), `think.Bool()` on and off, `think.Level()` for templates with a reasoning-strength string.

**Verify before Step 3:**

```sh
go build ./model/renderers/
```

### Step 3 — Write the renderer test

Table test with `go-cmp` on the full prompt string, following `model/renderers/lfm2_test.go`:

```go
func TestFooRenderer_ChatTemplateParity(t *testing.T) {
	tests := []struct {
		name       string
		renderer   *FooRenderer
		messages   []api.Message
		tools      []api.Tool
		thinkValue *api.ThinkValue
		expected   string
	}{
		{name: "user_only", renderer: &FooRenderer{}, messages: []api.Message{{Role: "user", Content: "Hello"}}, expected: "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.renderer.Render(tt.messages, tt.tools, tt.thinkValue)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.expected, got); diff != "" {
				t.Errorf("prompt mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
```

Required cases: `user_only`, `system_and_user`, `tools_*`, a full tool round-trip (assistant tool call → `tool` role result → next assistant turn), thinking on, thinking off. Build tool arguments and properties with `testArgsOrdered` / `testPropsOrdered` from `model/renderers/testhelpers_test.go` — never a literal map, or key order flaps between runs. Put an `// Ground truth ...` comment naming how the expectation was produced (HF jinja render with `add_generation_prompt=true`), as `model/renderers/cohere_test.go` does.

**Verify before Step 4:**

```sh
go test ./model/renderers/ -run TestFoo
```

### Step 4 — Write the parser file in `model/parsers/`

Uses the same tag constants re-declared in `package parsers` (the packages do not share consts; `cohereEndThinking` and friends live in `model/parsers/cohere.go` independently). Implement the full `Parser` interface from `model/parsers/parsers.go`:

```go
type FooParser struct {
	state     fooParserState
	buffer    strings.Builder
	callIndex int
	hasThinkingSupport bool
}

func (p *FooParser) Init(tools []api.Tool, lastMessage *api.Message, thinkValue *api.ThinkValue) []api.Tool
func (p *FooParser) Add(s string, done bool) (content string, thinking string, calls []api.ToolCall, err error)
func (p *FooParser) PreservedTokens() []string
func (p *FooParser) HasToolSupport() bool
func (p *FooParser) HasThinkingSupport() bool
func (p *FooParser) ThinkingTags() (string, string) // when thinking is delimited by literal tags
```

Rules from the existing parsers:

- **`Init` resets everything** (`p.buffer.Reset()`, `p.callIndex = 0`) and picks the start state from the generation prompt the renderer emitted: if the prompt ends *inside* a thinking tag, start in the thinking state; if `lastMessage` is an assistant prefill, start in the content state (see `model/parsers/cohere.go`). Return `tools` unchanged unless the format renames them.
- **`Add` buffers, then drains in a loop** over an `eat(done)` helper that returns `more=true` after each state transition, so one chunk can cross several tags. Assign `calls[i].Function.Index = p.callIndex++` after the loop.
- **Tags split across chunks must not leak.** Hold back a suffix using `overlap(s, delim)` from `model/parsers/parsers.go`, and use `splitAtTag` where a simple split suffices.
- **`PreservedTokens()` returns every literal the parser matches on** — `server/routes.go` passes it to the runner so llama-server keeps those tokens as text instead of eating them as specials. Missing entries look like "the parser never fires".
- **`ThinkingTags()` is required when `HasThinkingSupport()` is true and the block is tag-delimited** — it is what the carried thinking-budget patch uses to close a spent block (`ThinkingTagsForParser` in `model/parsers/parsers.go`). Return the open/close literals, e.g. `return fooStartThink, fooEndThink`.
- `HasToolSupport()` / `HasThinkingSupport()` also drive declared model capabilities in `create/metadata.go` — wrong values mean `tools` or `thinking` is missing from the model's capability list.

**Verify before Step 5:**

```sh
go build ./model/parsers/
```

### Step 5 — Write the parser test

Follow `model/parsers/cohere_test.go`: a local `fooAddAll(t, p, chunks []string)` helper that feeds chunks with `done` set on the last one and concatenates results. Required cases, named `TestFooParser<Behavior>`:

1. `TestFooParserThinkingThenText` — normal flow.
2. `TestFooParserSplitTags` — every tag deliberately split mid-literal across chunks (`"<|END_TH", "INKING|>"`). This is the test that catches the common bug.
3. `TestFooParserToolCall` — one and two calls, asserting `Function.Index` is 0,1.
4. `TestFooParserToolCallIndexResetOnInit` — call `Init` again, index restarts at 0.
5. Thinking-disabled case when the parser has a non-thinking variant.

Compare tool calls with `toolCallEqual` / `toolsComparer` from `model/parsers/testhelpers_test.go` (`cmp.Diff` on raw `api.ToolCall` fails on argument ordering and unexported fields).

**Verify before Step 6:**

```sh
go test ./model/parsers/ -run TestFoo
```

### Step 6 — Register both names

Uses the types from Steps 2 and 4. Either (fork-only, preferred):

```go
// in the new model/renderers/ file
func init() { Register("foo-1", func() Renderer { return &FooRenderer{useImgTags: RenderImgTags} }) }
// in the new model/parsers/ file
func init() { Register("foo-1", func() Parser { return &FooParser{} }) }
```

or (upstream-shaped) one case each, placed next to its sibling models:

```go
// model/renderers/renderer.go, in rendererForName
case "foo-1":
	return &FooRenderer{useImgTags: RenderImgTags}
case "foo-1-thinking":
	return &FooRenderer{useImgTags: RenderImgTags, isThinking: true}

// model/parsers/parsers.go, in ParserForName
case "foo-1":
	return &FooParser{}
```

Only add a mapping in `server/renderer_resolution.go` (`resolveRendererName`) when one published renderer name must fan out by model size or short name, as `gemma4` → `gemma4-small` / `gemma4-large` does.

**Verify before Step 7:**

```sh
go test ./model/renderers/ -run TestBuiltInRendererStillWorks
```

passes and `RenderWithRenderer("foo-1", …)` no longer returns `unknown renderer`.

### Step 7 — Full verification

```sh
go build ./...
go test ./model/... ./server/...
golangci-lint run
```

Optionally, for a jinja parity test modelled on `model/renderers/glimmer_reference_test.go`:

```sh
VERIFY_JINJA2=1 go test ./model/renderers/ -run Reference
```

(it shells out to a repo `.venv/bin/python` or `uv run --with 'transformers>=5,<6' python`, and skips otherwise).

### Step 8 — Repo bookkeeping

1. Add the four new files to `.wolf/anatomy.md` (2-3 line description + token estimate each).
2. Append a line to `.wolf/memory.md`:

   ```
   | HH:MM | added foo-1 renderer+parser | model/renderers/, model/parsers/ | tests pass | ~N |
   ```
3. Log any bug you hit while doing this to `.wolf/buglog.json` (tag it `renderer`, `parser`).
4. Commit message per `CONTRIBUTING.md`: `model/renderers: support the Foo-1 prompt format` — package prefix, lowercase continuation, no `feat:`.

## Examples

**User says:** "add support for the Foo-1 chat format, it has thinking and XML tool calls"

**Actions taken:**
1. Read the `chat_template` field from the checkpoint's tokenizer_config.json; saved the fixture under `model/renderers/testdata/`, recorded revision + SHA-256.
2. Created the renderer in `model/renderers/`: `const` block (`foo1BOS`, `foo1StartTurn`, `foo1Message`, `foo1EndTurn`, `foo1StartThink`, `foo1EndThink`, `foo1ToolsHeader`), `type Foo1Renderer struct{ useImgTags, isThinking bool }`, `LeadingBOS() → foo1BOS`, `Render` building with `strings.Builder` and `marshalWithSpaces` from `model/renderers/json.go` for the tool schema block, ending with `foo1StartTurn + "assistant" + foo1Message` (+ `foo1StartThink` when thinking is on).
3. Created its sibling test: `TestFoo1Renderer_ChatTemplateParity` with 7 table cases compared via `cmp.Diff`, tools built with `testPropsOrdered` from `model/renderers/testhelpers_test.go`.
4. Created the parser in `model/parsers/`: `Foo1Parser` with `foo1CollectingThinking → foo1AwaitingBlock → foo1CollectingContent / foo1CollectingToolCall` states, buffered `Add`/`eat` loop, `PreservedTokens` listing all six literals, `ThinkingTags() → foo1StartThink, foo1EndThink`, `HasToolSupport/HasThinkingSupport → true`.
5. Created the parser test with the five required cases including `TestFoo1ParserSplitTags`.
6. Added `case "foo-1":` to `rendererForName` in `model/renderers/renderer.go` and to `ParserForName` in `model/parsers/parsers.go`.
7. Ran `go test ./model/... ./server/...` and `golangci-lint run` — clean.
8. Updated `.wolf/anatomy.md` and `.wolf/memory.md`.

**Result:** a model created with `Renderer: "foo-1"` / `Parser: "foo-1"` renders the published template byte-for-byte, reports `tools` + `thinking` capabilities, and streams tool calls with correct indices. No existing model's behaviour changed.

## Common Issues

**`unknown renderer "foo-1"` from `/api/chat`** (raised in `model/renderers/renderer.go`, surfaced through `server/prompt.go`):
1. `grep -n 'foo-1' model/renderers/renderer.go` — the `case` or `Register` call is missing.
2. If present, the name in the model config differs. Check `resolveRendererName` in `server/renderer_resolution.go` — it can remap the configured name before lookup.
3. Confirm what the model actually carries: `ollama show --modelfile <model> | grep -i renderer`.

**Model streams raw tags like `<|think|>` into `content`:** the parser is nil or not selected. `ParserForName` returns `nil` for an unknown name and the server falls back to passthrough — `grep -n 'foo-1' model/parsers/parsers.go`. If the parser *is* selected, the missing literal is absent from `PreservedTokens()`, so llama-server consumed it as a special token before your `Add` saw it (see `server/routes.go`).

**Prompt starts with two BOS tokens (or one is missing):** `LeadingBOS()` and the first bytes of `Render` disagree. `llm/llama_server.go` only trims when `strings.HasPrefix(prompt, leadingBOS)` is exactly true. Make `LeadingBOS()` return the literal `Render` writes, or `""`.

**Tag text leaks into output only under streaming (passes when fed one chunk):** `Add` is matching on the current chunk instead of the accumulated buffer, or it flushes a trailing partial tag. Buffer everything and hold back `overlap(buf, tag)` bytes before emitting. Reproduce with the `SplitTags` test from Step 5.

**`model "tools" capability missing` / thinking not offered:** `HasToolSupport()` or `HasThinkingSupport()` returns false — `create/metadata.go` derives declared capabilities from the parser instance. Re-create the model after fixing; capabilities are baked at create time.

**Renderer test diff shows identical-looking strings:** it is whitespace. Print with `%q` (`t.Errorf("got: %q\nwant: %q", got, want)`) as `model/renderers/cohere_test.go` does — trailing newline vs. none inside a turn is the usual culprit.

**Test flakes on tool-argument order:** a plain `map[string]any` was used. Switch to `api.NewToolCallFunctionArguments()` + `.Set(...)`, or `testArgsOrdered` / `testPropsOrdered` from `model/renderers/testhelpers_test.go`; compare with `toolsComparer`.

**`cmp.Diff` panics with "cannot handle unexported field"** on `api.ToolCall` / `api.ToolPropertiesMap`: pass `toolsComparer` (parsers) or compare the rendered string instead of the structs (renderers).

**Reference jinja test skipped:** it is gated — run `VERIFY_JINJA2=1 go test ./model/renderers/ -run Reference`, and make `uv` available or create a repo-root `.venv` with `transformers>=5,<6`.

**`golangci-lint run` flags the new file only:** run `gofumpt -w` on the two new files first; the tree is formatted with the stricter rules already.
