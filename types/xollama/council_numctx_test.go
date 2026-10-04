package xollama

import (
	"strings"
	"testing"
)

// A role's num_ctx is the window of its own model: a role on the council's
// model cannot have one, and none may be negative.
func TestARoleWindowNeedsTheRolesOwnModel(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name string
		role CouncilRole
		err  string
	}{
		{"on its own model", CouncilRole{Model: "glm-5.3-flash:cloud", NumCtx: 262144}, ""},
		{"on the council's model", CouncilRole{NumCtx: 262144}, "needs council.critic.model"},
		{"negative", CouncilRole{Model: "glm-5.3-flash:cloud", NumCtx: -1}, "must not be negative"},
	} {
		role := tc.role
		c := &Config{Version: SchemaVersion, Council: &Council{Enabled: &yes, Critic: &role}}
		err := c.Validate()
		if tc.err == "" && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: error %v, want one containing %q", tc.name, err, tc.err)
		}
	}
}

// A role's num_ctx raises the schema floor to v5: an older build would drop it
// and run the role in its template's window. A council without one stays v4.
func TestARoleWindowIsSchemaFive(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		role *CouncilRole
		want int
	}{
		{&CouncilRole{Model: "glm-5.3-flash:cloud", MaxTokens: 131072}, 4},
		{&CouncilRole{Model: "glm-5.3-flash:cloud", NumCtx: 262144}, 5},
	} {
		b, err := (&Config{Council: &Council{Enabled: &yes, Researcher: tc.role}}).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		out, err := Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		if out.Version != tc.want {
			t.Errorf("%+v: written as v%d, want v%d", *tc.role, out.Version, tc.want)
		}
	}
}
