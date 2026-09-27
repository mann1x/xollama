package council

// Shared reads (owner's ruling 2026-09-27, after ab-3). Within a turn, a read
// one member made is not made again: a member that asks for the same thing
// -- the same read-only tool with the same arguments -- is answered in place
// from the result already in the turn, and the call never reaches the client.
// Nothing is pushed into a member that did not ask. A write clears it all:
// what was read before it may no longer hold. Measured in ab-3: 12 full-file
// reads where the plain chat made 3, two of them the same file read by two
// researchers in the same round.

import (
	"encoding/json"
	"slices"

	"github.com/ollama/ollama/api"
)

// SharedReads indexes the reads a turn has made so far, from the client's
// history after its last user message: each read-only call's key (readKey)
// gives the forwarded id of the call whose result answers it. A result of a
// call that may write empties the index.
func SharedReads(tools api.Tools, tail []api.Message) map[string]string {
	calls := map[string]api.ToolCall{}
	out := map[string]string{}
	var open []string
	for _, m := range tail {
		switch m.Role {
		case "assistant":
			open = nil
			for _, c := range m.ToolCalls {
				calls[c.ID] = c
				open = append(open, c.ID)
			}
		case "tool":
			id := m.ToolCallID
			if id == "" && len(open) > 0 {
				id = open[0]
			}
			open = slices.DeleteFunc(open, func(s string) bool { return s == id })
			c, ok := calls[id]
			if !ok {
				continue
			}
			if !readOnly(tools, c) {
				clear(out)
				continue
			}
			if k := readKey(c); out[k] == "" {
				out[k] = id
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readOnly reports whether c calls one of the client's read-only tools.
func readOnly(tools api.Tools, c api.ToolCall) bool {
	if local(c) {
		return false
	}
	i := slices.IndexFunc(tools, func(t api.Tool) bool { return t.Function.Name == c.Function.Name })
	return i >= 0 && tools[i].Function.ReadOnly
}

// readKey is what makes two reads the same: the tool and its arguments,
// whatever order the model wrote them in.
func readKey(c api.ToolCall) string {
	var args any
	raw, _ := json.Marshal(c.Function.Arguments)
	if json.Unmarshal(raw, &args) == nil {
		raw, _ = json.Marshal(args) // maps marshal with sorted keys
	}
	return c.Function.Name + "\x00" + string(raw)
}

// cachedRead reports whether c is a read this turn has already made.
func (cfg Config) cachedRead(c api.ToolCall) bool {
	src, ok := cfg.Reads[readKey(c)]
	if !ok || !readOnly(cfg.Tools, c) {
		return false
	}
	_, ok = cfg.Results[src]
	return ok
}

// result is the result of a member's call and the ref that names it: the
// client's answer to this very call, or else the answer to the same read
// made earlier in the turn, under that call's ref.
func (cfg Config) result(key string, c api.ToolCall) (string, string, bool) {
	ref := ForwardedID(key, c.ID)
	if s, ok := cfg.Results[ref]; ok {
		return s, ref, true
	}
	if cfg.cachedRead(c) {
		src := cfg.Reads[readKey(c)]
		return cfg.Results[src], src, true
	}
	return "", ref, false
}

// once drops, from the calls a step forwards, every read that repeats one
// before it: the client runs it once, and the next request answers each
// member that asked from that one result.
func once(tools api.Tools, calls []api.ToolCall) []api.ToolCall {
	seen := map[string]bool{}
	return slices.DeleteFunc(calls, func(c api.ToolCall) bool {
		if !readOnly(tools, c) {
			return false
		}
		k := readKey(c)
		if seen[k] {
			return true
		}
		seen[k] = true
		return false
	})
}
