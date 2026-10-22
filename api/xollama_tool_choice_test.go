package api

import (
	"encoding/json"
	"testing"
)

func TestToolChoiceNone(t *testing.T) {
	for raw, want := range map[string]bool{
		``:                                 false,
		`"none"`:                           true,
		`"auto"`:                           false,
		`"required"`:                       false,
		`{"type":"none"}`:                  true,
		`{"type":"auto"}`:                  false,
		`{"type":"function","name":"get"}`: false,
		`null`:                             false,
	} {
		if got := ToolChoiceNone(json.RawMessage(raw)); got != want {
			t.Errorf("ToolChoiceNone(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestChatRequestCarriesToolChoice(t *testing.T) {
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"m","tool_choice":"none"}`), &req); err != nil {
		t.Fatal(err)
	}
	if !ToolChoiceNone(req.ToolChoice) {
		t.Fatalf("tool_choice %s was not read as none", req.ToolChoice)
	}
}
