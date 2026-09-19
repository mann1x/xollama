package xollama

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Config
	}{
		{
			name: "version only",
			in:   `{"version":1}`,
			want: Config{Version: 1},
		},
		{
			name: "engine pin",
			in:   `{"version":1,"engine":"opencoti"}`,
			want: Config{Version: 1, Engine: "opencoti"},
		},
		{
			name: "draft spec type",
			in:   `{"version":1,"draft":{"spec_type":"draft-assistant"}}`,
			want: Config{Version: 1, Draft: &Draft{SpecType: "draft-assistant"}},
		},
		{
			// A newer build may add fields. A model mentioning one this build
			// does not have is still servable, so unknown KEYS are accepted.
			name: "unknown field is accepted",
			in:   `{"version":1,"engine":"llamacpp","something_new":{"a":1}}`,
			want: Config{Version: 1, Engine: "llamacpp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.want.Version || got.Engine != tt.want.Engine {
				t.Fatalf("Parse = %+v, want %+v", *got, tt.want)
			}
			if (got.Draft == nil) != (tt.want.Draft == nil) {
				t.Fatalf("Parse draft = %+v, want %+v", got.Draft, tt.want.Draft)
			}
			if got.Draft != nil && got.Draft.SpecType != tt.want.Draft.SpecType {
				t.Fatalf("Parse spec_type = %q, want %q", got.Draft.SpecType, tt.want.Draft.SpecType)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "no version", in: `{"engine":"opencoti"}`, wantErr: "missing or invalid version"},
		{name: "zero version", in: `{"version":0}`, wantErr: "missing or invalid version"},
		{name: "negative version", in: `{"version":-1}`, wantErr: "missing or invalid version"},
		{
			// The fields here change how a model is served. Reading a future
			// schema as if it were this one would serve the model differently
			// from how its publisher meant, and say nothing.
			name:    "newer schema",
			in:      `{"version":2}`,
			wantErr: "newer than this build understands",
		},
		{name: "unknown engine", in: `{"version":1,"engine":"vllm"}`, wantErr: `unknown engine "vllm"`},
		{name: "auto is not an engine", in: `{"version":1,"engine":"auto"}`, wantErr: `unknown engine "auto"`},
		{name: "unknown spec type", in: `{"version":1,"draft":{"spec_type":"draft-nope"}}`, wantErr: "unknown draft.spec_type"},
		{name: "not json", in: `{`, wantErr: "xollama config:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatalf("Parse(%s) succeeded, want error containing %q", tt.in, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Marshal stamps the version so a caller cannot write an unversioned blob by
// forgetting to set it — an unversioned blob is unreadable by Parse.
func TestMarshalStampsVersion(t *testing.T) {
	c := &Config{Engine: "opencoti"}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != float64(SchemaVersion) {
		t.Fatalf("marshalled version = %v, want %d", raw["version"], SchemaVersion)
	}
	if c.Version != 0 {
		t.Fatalf("Marshal mutated its receiver: version = %d, want 0", c.Version)
	}
	round, err := Parse(data)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if round.Engine != "opencoti" {
		t.Fatalf("round trip engine = %q, want opencoti", round.Engine)
	}
}

func TestMarshalRejectsInvalid(t *testing.T) {
	if _, err := (&Config{Engine: "nope"}).Marshal(); err == nil {
		t.Fatal("Marshal accepted an unknown engine")
	}
}

func TestIsZero(t *testing.T) {
	tests := []struct {
		name string
		c    *Config
		want bool
	}{
		{name: "nil", c: nil, want: true},
		{name: "empty", c: &Config{Version: 1}, want: true},
		{name: "empty draft struct", c: &Config{Version: 1, Draft: &Draft{}}, want: true},
		{name: "engine set", c: &Config{Version: 1, Engine: "opencoti"}, want: false},
		{name: "spec type set", c: &Config{Version: 1, Draft: &Draft{SpecType: "draft-mtp"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.IsZero(); got != tt.want {
				t.Fatalf("IsZero = %v, want %v", got, tt.want)
			}
		})
	}
}

// A shared prefix pool attaches to a session, so a model asking for a pool
// while switching session identity off is asking for something that cannot
// work. Catching it at create time is better than serving a model whose stated
// configuration silently does nothing.
func TestValidateSessionPoolRequiresAffinity(t *testing.T) {
	no, yes := false, true

	if err := (&Config{Version: 1, Session: &Session{Pool: &yes, Affinity: &no}}).Validate(); err == nil {
		t.Error("expected session.pool with session.affinity=false to be refused")
	}
	for _, tt := range []struct {
		name string
		s    *Session
	}{
		{"pool with affinity on", &Session{Pool: &yes, Affinity: &yes}},
		{"pool with affinity unstated, which leaves it to the server default", &Session{Pool: &yes}},
		{"affinity off on its own", &Session{Affinity: &no}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := (&Config{Version: 1, Session: tt.s}).Validate(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// A session block round-trips through the layer, and a config carrying only a
// session block is not "empty" -- writing no layer for it would drop the
// setting on publish.
func TestSessionRoundTripAndIsZero(t *testing.T) {
	yes := true
	c := &Config{Version: 1, Session: &Session{Affinity: &yes, Pool: &yes}}
	if c.IsZero() {
		t.Fatal("a config carrying a session block must be stored")
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Session == nil || got.Session.Affinity == nil || !*got.Session.Affinity ||
		got.Session.Pool == nil || !*got.Session.Pool {
		t.Errorf("session did not survive the round trip: %s", data)
	}
	if (&Config{Version: 1, Session: &Session{}}).IsZero() != true {
		t.Error("a session block stating nothing carries nothing worth storing")
	}
}
