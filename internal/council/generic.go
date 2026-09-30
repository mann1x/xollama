package council

import (
	"slices"
	"strings"
	"unicode"

	"github.com/ollama/ollama/api"
)

// A generic harness drives a council model as it drives any model: it marks
// no tool read-only (function.x_read_only) and states no check. The council
// infers both, topic-agnostically, from what any harness sends: tool names
// and the member's own calls.

// Words that make a tool a reader, and words that make it a writer whatever
// else its name says ("read_and_write", "run_search").
var (
	readWords  = []string{"read", "list", "ls", "grep", "search", "find", "glob", "get", "fetch", "view", "show", "cat", "stat", "lookup", "query", "inspect", "describe", "extract", "lsp"}
	writeWords = []string{"write", "edit", "create", "delete", "remove", "rm", "run", "exec", "execute", "command", "commands", "shell", "bash", "apply", "patch", "update", "set", "move", "mv", "rename", "replace", "insert", "commit", "push", "install", "save", "put", "post", "send", "kill", "browser", "click"}
)

// InferReadOnly marks the tools whose names say they only read, when the
// client marked none: a client that marks any is taken at its word. Only
// researchers and critics are limited by it, to the tools marked; nothing the
// client runs changes.
func InferReadOnly(tools api.Tools) api.Tools {
	if len(tools) == 0 || slices.ContainsFunc(tools, func(t api.Tool) bool { return t.Function.ReadOnly }) {
		return tools
	}
	out := slices.Clone(tools)
	for i := range out {
		out[i].Function.ReadOnly = readsByName(out[i].Function.Name)
	}
	return out
}

func readsByName(name string) bool {
	words := nameWords(name)
	if slices.ContainsFunc(words, func(w string) bool { return slices.Contains(writeWords, w) }) {
		return false
	}
	return slices.ContainsFunc(words, func(w string) bool { return slices.Contains(readWords, w) })
}

// nameWords splits a tool name at separators and case changes, lowercased:
// "readFiles", "read_files" and "read-files" are all read, files.
func nameWords(name string) []string {
	var words []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			words = append(words, strings.ToLower(b.String()))
			b.Reset()
		}
	}
	prev := rune(0)
	for _, r := range name {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
		prev = r
	}
	flush()
	return words
}

// inferredCheck is the check of a member no harness told it: the first call
// that changes nothing it did not change before -- a write its tool marks,
// sent again with the same arguments after another change. An edit differs
// each time; running the tests, the build or the program does not. Nil until
// the member has repeated one.
func (cfg Config) inferredCheck(turns []api.Message) *api.ToolCall {
	seen := map[string]int{} // readKey -> the change count when it was last made
	changes := 0
	for _, t := range turns {
		for _, c := range t.ToolCalls {
			if local(c) || readOnly(cfg.Tools, c) {
				continue
			}
			k := readKey(c)
			if n, ok := seen[k]; ok && changes > n {
				found := api.ToolCall{Function: api.ToolCallFunction{Name: c.Function.Name, Arguments: c.Function.Arguments}}
				return &found
			}
			changes++
			seen[k] = changes
		}
	}
	return nil
}
