package parsers

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func gemma4EditorTool() []api.Tool {
	props := api.NewToolPropertiesMap()
	props.Set("path", api.ToolProperty{Type: api.PropertyType{"string"}})
	props.Set("content", api.ToolProperty{Type: api.PropertyType{"string"}})
	return []api.Tool{{Type: "function", Function: api.ToolFunction{
		Name: "editor",
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Required:   []string{"path", "content"},
			Properties: props,
		},
	}}}
}

// The model writes the next argument's name inside a string and closes the
// string after it (opencoti bug-3541; 2 in 91 calls in a pandorum swarm). The
// delimiters balance, so the call used to parse: content ended in ",path:"
// and path was missing.
func TestGemma4SwallowedKeyIsRejected(t *testing.T) {
	cases := map[string]string{
		"top level":           `call:editor{content:<|"|>let a = 1;,path:<|"|>}`,
		"after a backtick":    "call:editor{content:<|\"|>see `x`,path:<|\"|>}",
		"trailing whitespace": `call:editor{content:<|"|>let a = 1;,path:  <|"|>}`,
		"nested object":       `call:editor{path:<|"|>a.js<|"|>,opts:{mode:<|"|>w,flag:<|"|>}}`,
		"object in an array":  `call:editor{path:<|"|>a.js<|"|>,edits:[{old:<|"|>x,new:<|"|>}]}`,
		"undeclared name":     `call:editor{path:<|"|>a.js<|"|>,content:<|"|>x,mode:<|"|>}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			call, err := parseGemma4ToolCall(input, gemma4EditorTool())
			if err == nil {
				t.Fatalf("accepted %q as %v", input, call.Function.Arguments.ToMap())
			}
			if !strings.Contains(err.Error(), "rejected, not repaired") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Values that only look like the shape must still parse.
func TestGemma4SwallowedKeyLeavesRealValuesAlone(t *testing.T) {
	cases := map[string]string{
		// The name is a key of the same object: the model wrote it properly.
		"key present": `call:editor{content:<|"|>x,path:<|"|>,path:<|"|>a.js<|"|>}`,
		// Prose puts a space after the comma.
		"prose": `call:editor{path:<|"|>a.md<|"|>,content:<|"|>first, then:<|"|>}`,
		// Minified code with more after the colon.
		"minified js": `call:editor{path:<|"|>a.js<|"|>,content:<|"|>{x:1,y:2}<|"|>}`,
		// A digit-leading name is not an argument name.
		"digit name": `call:editor{path:<|"|>a.txt<|"|>,content:<|"|>10,20:<|"|>}`,
		"ordinary":   `call:editor{path:<|"|>a.js<|"|>,content:<|"|>let a = 1;<|"|>}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGemma4ToolCall(input, gemma4EditorTool()); err != nil {
				t.Fatalf("rejected %q: %v", input, err)
			}
		})
	}
}
