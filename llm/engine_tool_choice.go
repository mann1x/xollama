package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"unicode"
)

// xollama-hook: tool-choice — see docs/xollama/tool-choice.mdx
//
// tool_choice "none" on a request that still carries its tools (cerebriline,
// mail #1733; owner 2026-10-22). The tools stay in the prompt so the client's
// prefix is reused, and the model must not open a call: on pandorum a side
// call that said not to use them wrote an editor call to its 4000-token cap.
// Nothing here needs the engine changed: both engines take logit_bias, stop
// and, on the chat path, tool_choice.

// toolCallBan is what keeps a model from opening a tool call on the completion
// path. The tag a call opens with is banned from sampling when the engine reads
// it as one token (<|tool_call>, <tool_call>, [TOOL_CALLS]), so the model goes
// on in plain text. A tag of several tokens cannot be banned without banning
// its pieces wherever they occur, so the answer ends where it starts instead,
// said at Warn.
type toolCallBan struct {
	logitBias []any
	stop      string
}

// banToolCall works out the ban for tag with the engine's own tokenizer. A tag
// with no letter in it ("{", the generic parser's JSON opener) is ordinary
// text, never a delimiter, and is left alone: banning it would ban every brace.
func (s *llamaServerRunner) banToolCall(ctx context.Context, tag string) toolCallBan {
	if !delimiterTag(tag) {
		return toolCallBan{}
	}
	ids, err := s.tokenizeSpecial(ctx, tag)
	if err == nil && len(ids) == 1 {
		return toolCallBan{logitBias: []any{[]any{ids[0], false}}}
	}
	slog.Warn("tool_choice none: the tool-call tag is not one token, so the answer ends where a call would start",
		"tag", tag, "tokens", len(ids), "error", err)
	return toolCallBan{stop: tag}
}

// apply adds the ban to a completion request.
func (b toolCallBan) apply(req *llamaServerCompletionRequest) {
	if b.logitBias != nil {
		req.LogitBias = b.logitBias
	}
	if b.stop != "" {
		req.Stop = append(append([]string(nil), req.Stop...), b.stop)
	}
}

func delimiterTag(tag string) bool {
	for _, r := range tag {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// tokenizeSpecial tokenizes text as the engine reads a special token in a
// prompt (parse_special), with nothing added in front.
func (s *llamaServerRunner) tokenizeSpecial(ctx context.Context, text string) ([]int, error) {
	body, err := json.Marshal(map[string]any{
		"content":       text,
		"add_special":   false,
		"parse_special": true,
	})
	if err != nil {
		return nil, err
	}
	status, out, err := s.postPool(ctx, "/tokenize", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("engine refused to tokenize %q: %s", text, http.StatusText(status))
	}
	var res tokenizeResponse
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return res.Tokens, nil
}

// chatToolChoice is the chat path's tool_choice: llama-server renders the
// template there and honours "none" itself. Nil, and so absent from the body,
// unless the request forbids calls and carries tools.
func chatToolChoice(req ChatRequest) any {
	if req.ToolChoiceNone && len(req.Tools) > 0 {
		return "none"
	}
	return nil
}
