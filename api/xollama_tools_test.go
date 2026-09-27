package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAToolCarriesItsReadOnlyMark(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"type":"function","function":{"name":"read_files","x_read_only":true,"parameters":{"type":"object"}}}`, true},
		{`{"type":"function","function":{"name":"grep","annotations":{"readOnlyHint":true}}}`, true},
		{`{"type":"function","function":{"name":"write","annotations":{"readOnlyHint":true},"x_read_only":false}}`, false},
		{`{"type":"function","function":{"name":"write","parameters":{"type":"object"}}}`, false},
		{`{"type":"function","function":{"name":"odd","x_read_only":"yes"}}`, false},
	} {
		var tool Tool
		if err := json.Unmarshal([]byte(tc.body), &tool); err != nil {
			t.Fatal(err)
		}
		if tool.Function.ReadOnly != tc.want {
			t.Errorf("%s: read-only %v, want %v", tc.body, tool.Function.ReadOnly, tc.want)
		}
	}
}

// The mark never reaches a prompt: a tool renders as upstream renders it.
func TestTheReadOnlyMarkIsNeverRendered(t *testing.T) {
	var tool Tool
	if err := json.Unmarshal([]byte(`{"type":"function","function":{"name":"read_files","description":"d","x_read_only":true,"parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`), &tool); err != nil {
		t.Fatal(err)
	}
	if s := tool.String(); strings.Contains(s, "read_only") || strings.Contains(s, "ReadOnly") {
		t.Fatalf("rendered %s", s)
	}
	var plain Tool
	_ = json.Unmarshal([]byte(`{"type":"function","function":{"name":"read_files","description":"d","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`), &plain)
	if tool.String() != plain.String() {
		t.Fatalf("a marked tool renders %s, the same tool unmarked %s", tool.String(), plain.String())
	}
}
