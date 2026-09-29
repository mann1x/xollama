package parser

import (
	"errors"
	"strings"
	"testing"
)

// The Modelfile of omnimerge-v4-mtp_tb:27b-iq2m-128k (2026-09-29): an open
// quote on TEMPLATE swallowed RENDERER, PARSER and three PARAMETERs up to the
// next quote, and the model was created without any of them.
func TestParseFileRefusesSwallowedDirectives(t *testing.T) {
	input := "FROM model.gguf\n" +
		"TEMPLATE \"{{ .Prompt }}\n" +
		"RENDERER qwen3.5\n" +
		"PARSER qwen3.5\n" +
		"PARAMETER repeat_penalty 1\n" +
		"PARAMETER think_budget medium\n" +
		"PARAMETER think_budget_message \"\n" +
		"PARAMETER top_k 20\n"

	_, err := ParseFile(strings.NewReader(input))
	var perr *ParserError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a ParserError", err)
	}
	if !strings.Contains(perr.Msg, `"RENDERER qwen3.5"`) || !strings.Contains(perr.Msg, "TEMPLATE") {
		t.Fatalf("message %q should name TEMPLATE and the first swallowed line", perr.Msg)
	}
}

func TestParseFileKeepsMultilineTemplates(t *testing.T) {
	for _, input := range []string{
		"FROM m\nTEMPLATE \"\"\"{{ if .System }}SYSTEM: {{ .System }}\n{{ end }}USER: {{ .Prompt }}\nASSISTANT:\"\"\"\n",
		"FROM m\nTEMPLATE \"{{ .System }}\n### Instruction:\n{{ .Prompt }}\"\nPARAMETER top_k 20\n",
		"FROM m\nSYSTEM \"\"\"You are a parser.\nFrom now on answer briefly.\"\"\"\n",
	} {
		f, err := ParseFile(strings.NewReader(input))
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if len(f.Commands) < 2 {
			t.Fatalf("%q: commands = %v", input, f.Commands)
		}
	}
}

// A think_budget_message of several lines with quotes in it goes through
// Command.String and back unchanged.
func TestMultilineParameterRoundTrips(t *testing.T) {
	msg := "\nConsidering the limited time, I have to give the solution now.\nSay \"done\" when finished.\n"
	mf := Modelfile{Commands: []Command{
		{Name: "model", Args: "m.gguf"},
		{Name: "think_budget_message", Args: msg},
		{Name: "top_k", Args: "20"},
	}}
	f, err := ParseFile(strings.NewReader(mf.String()))
	if err != nil {
		t.Fatalf("%v\n%s", err, mf.String())
	}
	if got := f.Commands[1].Args; got != msg {
		t.Fatalf("think_budget_message = %q, want %q\n%s", got, msg, mf.String())
	}
	if f.Commands[2].Name != "top_k" {
		t.Fatalf("commands after the message = %v", f.Commands[2:])
	}
}
