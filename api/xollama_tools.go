package api

// xollama: a tool's read-only mark, for the council's tool policy -- see
// plans/agentic-council-chat.md (9.5). Additive: the field it fills is the
// `council` hook in types.go.

import "encoding/json"

// UnmarshalJSON reads a tool function as upstream does, plus the client's
// read-only mark: `x_read_only`, or MCP's `annotations.readOnlyHint` for a
// client that passes an MCP tool through as it is. A tool with neither is
// taken to change things.
func (t *ToolFunction) UnmarshalJSON(b []byte) error {
	type plain ToolFunction
	var f plain
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	var mark struct {
		ReadOnly    *bool `json:"x_read_only"`
		Annotations struct {
			ReadOnlyHint *bool `json:"readOnlyHint"`
		} `json:"annotations"`
	}
	_ = json.Unmarshal(b, &mark) // the fields were read above; a mark of the wrong type is no mark
	*t = ToolFunction(f)
	switch {
	case mark.ReadOnly != nil:
		t.ReadOnly = *mark.ReadOnly
	case mark.Annotations.ReadOnlyHint != nil:
		t.ReadOnly = *mark.Annotations.ReadOnlyHint
	}
	return nil
}
