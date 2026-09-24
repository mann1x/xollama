package parsers

import (
	"testing"

	"github.com/ollama/ollama/api"
)

// LFM2.5-8B-A1B is reasoning-tuned and its chat template has no switch to stop
// it, so it reasons even when a request turns thinking off. Measured against
// the model on a live server: "think": false returned the whole
// <think>...</think> block verbatim in content.
func runLFM2ThinkOff(t *testing.T, p *LFM2Parser, think *api.ThinkValue, tools []api.Tool, chunks ...string) (content, thinking string, calls []api.ToolCall) {
	t.Helper()
	p.Init(tools, nil, think)
	for i, c := range chunks {
		c, th, tc, err := p.Add(c, i == len(chunks)-1)
		if err != nil {
			t.Fatal(err)
		}
		content += c
		thinking += th
		calls = append(calls, tc...)
	}
	return content, thinking, calls
}

func TestLFM2ThinkOffKeepsReasoningOutOfTheAnswer(t *testing.T) {
	for _, think := range []*api.ThinkValue{{Value: false}, nil} {
		content, thinking, _ := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: true}, think, nil,
			"<think> We need to explain caches.</think>\n\nA CPU cache is small and fast.")
		if content != "A CPU cache is small and fast." {
			t.Errorf("think=%v: content = %q, want only the answer", think, content)
		}
		if thinking != "" {
			t.Errorf("think=%v: thinking = %q, want none reported when thinking is off", think, thinking)
		}
	}
}

func TestLFM2ThinkOffDiscardsReasoningSplitAcrossChunks(t *testing.T) {
	content, thinking, _ := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: true}, &api.ThinkValue{Value: false}, nil,
		"<th", "ink>", " reasoning", " more </thi", "nk>", "The answer", " is 42.")
	if content != "The answer is 42." || thinking != "" {
		t.Fatalf("content = %q, thinking = %q", content, thinking)
	}
}

func TestLFM2ThinkOffDirectAnswerIsUntouched(t *testing.T) {
	content, _, _ := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: true}, &api.ThinkValue{Value: false}, nil,
		"<", "b>bold</b> answer")
	if content != "<b>bold</b> answer" {
		t.Fatalf("a direct answer must pass through whole, got %q", content)
	}
}

func TestLFM2ThinkOffStillReturnsTheToolCallAfterReasoning(t *testing.T) {
	tools := []api.Tool{{Type: "function", Function: api.ToolFunction{Name: "get_weather"}}}
	content, thinking, calls := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: true}, &api.ThinkValue{Value: false}, tools,
		`<think>Check the weather.</think>`, `<|tool_call_start|>[get_weather(location="Paris")]<|tool_call_end|>`)
	if len(calls) != 1 || calls[0].Function.Name != "get_weather" {
		t.Fatalf("calls = %+v", calls)
	}
	if thinking != "" || content != "" {
		t.Fatalf("content = %q, thinking = %q", content, thinking)
	}
}

func TestLFM2WithoutThinkingSupportIsUnchanged(t *testing.T) {
	// The plain "lfm2" parser has no notion of a thinking block; what it
	// passed through before, it still passes through.
	content, _, _ := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: false}, &api.ThinkValue{Value: false}, nil,
		"<think>x</think>answer")
	if content != "<think>x</think>answer" {
		t.Fatalf("content = %q", content)
	}
}

func TestLFM2ThinkOnIsUnchanged(t *testing.T) {
	content, thinking, _ := runLFM2ThinkOff(t, &LFM2Parser{hasThinkingSupport: true}, &api.ThinkValue{Value: true}, nil,
		"<think>reasoning</think>answer")
	if thinking != "reasoning" || content != "answer" {
		t.Fatalf("content = %q, thinking = %q", content, thinking)
	}
}
