package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseXollamaDirective(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cfg.json"), []byte(`{"version":1,"engine":"opencoti"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		modelfile  string
		wantEngine string
		wantSpec   string
	}{
		{
			name:       "inline json",
			modelfile:  "FROM foo\nXOLLAMA {\"version\":1,\"engine\":\"opencoti\"}\n",
			wantEngine: "opencoti",
		},
		{
			name:      "path to a json file beside the Modelfile",
			modelfile: "FROM foo\nXOLLAMA cfg.json\n",
			// The file, not the literal string, is what gets parsed.
			wantEngine: "opencoti",
		},
		{
			name:      "multi-line inline json",
			modelfile: "FROM foo\nXOLLAMA \"\"\"{\n  \"version\": 1,\n  \"draft\": {\"spec_type\": \"draft-assistant\"}\n}\"\"\"\n",
			wantSpec:  "draft-assistant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf, err := ParseFile(strings.NewReader(tt.modelfile))
			if err != nil {
				t.Fatal(err)
			}
			req, err := mf.CreateRequest(dir)
			if err != nil {
				t.Fatal(err)
			}
			if req.Xollama == nil {
				t.Fatal("CreateRequest produced no xollama config")
			}
			if req.Xollama.Engine != tt.wantEngine {
				t.Fatalf("engine = %q, want %q", req.Xollama.Engine, tt.wantEngine)
			}
			gotSpec := ""
			if req.Xollama.Draft != nil {
				gotSpec = req.Xollama.Draft.SpecType
			}
			if gotSpec != tt.wantSpec {
				t.Fatalf("spec_type = %q, want %q", gotSpec, tt.wantSpec)
			}
		})
	}
}

// A bad config is reported while parsing the Modelfile, not later at create
// time, so the error points at the line that caused it.
func TestParseXollamaDirectiveRejects(t *testing.T) {
	tests := []struct {
		name      string
		modelfile string
		wantErr   string
	}{
		{name: "unknown engine", modelfile: "FROM foo\nXOLLAMA {\"version\":1,\"engine\":\"vllm\"}\n", wantErr: "unknown engine"},
		{name: "no version", modelfile: "FROM foo\nXOLLAMA {\"engine\":\"opencoti\"}\n", wantErr: "missing or invalid version"},
		{name: "future schema", modelfile: "FROM foo\nXOLLAMA {\"version\":99}\n", wantErr: "newer than this build understands"},
		{name: "malformed json", modelfile: "FROM foo\nXOLLAMA {\"version\":\n", wantErr: "xollama config:"},
		{name: "missing file", modelfile: "FROM foo\nXOLLAMA nope.json\n", wantErr: "XOLLAMA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf, err := ParseFile(strings.NewReader(tt.modelfile))
			if err != nil {
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseFile error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			_, err = mf.CreateRequest(t.TempDir())
			if err == nil {
				t.Fatalf("CreateRequest succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CreateRequest error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// A Modelfile has to survive a round trip through String(), which `ollama show
// --modelfile` prints. Without the String() case the directive would silently
// vanish from a shown Modelfile.
func TestXollamaDirectiveRoundTrips(t *testing.T) {
	const src = "FROM foo\nXOLLAMA {\"version\":1,\"engine\":\"opencoti\"}\n"
	mf, err := ParseFile(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	printed := mf.String()
	if !strings.Contains(printed, "XOLLAMA") {
		t.Fatalf("String() dropped the directive:\n%s", printed)
	}
	again, err := ParseFile(strings.NewReader(printed))
	if err != nil {
		t.Fatalf("reparsing printed Modelfile: %v\n%s", err, printed)
	}
	req, err := again.CreateRequest(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if req.Xollama == nil || req.Xollama.Engine != "opencoti" {
		t.Fatalf("round trip lost the config: %+v\nprinted:\n%s", req.Xollama, printed)
	}
}
