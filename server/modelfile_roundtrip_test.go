package server

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/template"
	"github.com/ollama/ollama/types/xollama"
)

// `show --modelfile` is how people derive a new Modelfile from an existing
// model, so anything it invents gets fed back to `create` and becomes real.
func TestAModelfileStatesNoTemplateWhenTheModelDefinesNone(t *testing.T) {
	// GetModel seeds every model with DefaultTemplate so the serving path
	// always has something to render with, so a nil check here would never
	// fire. This is that state.
	m := &Model{ModelPath: "/blobs/sha256-abc", Template: template.DefaultTemplate}

	got := m.String()
	if strings.Contains(got, "TEMPLATE") {
		t.Fatalf("a model with no template layer stated one:\n%s", got)
	}
	if !strings.Contains(got, "FROM /blobs/sha256-abc") {
		t.Fatalf("lost the FROM line:\n%s", got)
	}
}

// The other half: a model that really does define one must still say so, or
// deriving a Modelfile would lose it.
func TestAModelfileStatesTheTemplateWhenTheModelHasOne(t *testing.T) {
	tmpl, err := template.Parse("{{ .System }}|{{ .Prompt }}")
	if err != nil {
		t.Fatal(err)
	}
	m := &Model{ModelPath: "/blobs/sha256-abc", Template: tmpl, HasGoTemplate: true}

	got := m.String()
	if !strings.Contains(got, "TEMPLATE") {
		t.Fatalf("a model with a template layer stated none:\n%s", got)
	}
	if !strings.Contains(got, "{{ .System }}|{{ .Prompt }}") {
		t.Fatalf("stated a different template than the model carries:\n%s", got)
	}
}

// The config layer was the one part of a model that `show --modelfile` dropped,
// so a derived Modelfile rebuilt a model served differently from its source.
func TestAModelfileCarriesTheXollamaConfig(t *testing.T) {
	yes := true
	m := &Model{
		ModelPath: "/blobs/sha256-abc",
		Template:  template.DefaultTemplate,
		Xollama: &xollama.Config{
			Version: 1,
			Engine:  "opencoti",
			KV:      &xollama.KV{K: "kvarn2", V: "kvarn2"},
			DCA:     &xollama.DCA{Enabled: &yes},
		},
	}

	got := m.String()
	line := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "XOLLAMA ") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no XOLLAMA line:\n%s", got)
	}
	for _, want := range []string{`"engine":"opencoti"`, `"k":"kvarn2"`, `"enabled":true`} {
		if !strings.Contains(line, want) {
			t.Fatalf("XOLLAMA line is missing %s:\n%s", want, line)
		}
	}
	// One line, so it round-trips through the parser's inline-JSON form.
	if strings.Contains(strings.TrimPrefix(line, "XOLLAMA "), "\n") {
		t.Fatalf("XOLLAMA JSON spans lines:\n%s", line)
	}
}

// A model with no config states none. An empty XOLLAMA line would be read back
// as "remove the parent's config", which is the opposite of saying nothing.
func TestAModelfileStatesNoXollamaConfigWhenTheModelHasNone(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *xollama.Config
	}{
		{"nil", nil},
		{"present but empty", &xollama.Config{Version: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := &Model{ModelPath: "/blobs/sha256-abc", Template: template.DefaultTemplate, Xollama: tt.cfg}
			if got := m.String(); strings.Contains(got, "XOLLAMA") {
				t.Fatalf("stated a config the model does not carry:\n%s", got)
			}
		})
	}
}

// The stored version is recomputed on the way out, so a Modelfile derived from
// a model whose v2 fields are gone does not claim v2 on the rebuild.
func TestTheStatedConfigVersionIsTheOneARebuildWouldStore(t *testing.T) {
	m := &Model{
		ModelPath: "/blobs/sha256-abc",
		Template:  template.DefaultTemplate,
		Xollama:   &xollama.Config{Version: xollama.SchemaVersion, KV: &xollama.KV{K: "q8_0", V: "q8_0"}},
	}
	got := m.String()
	if !strings.Contains(got, `"version":1`) {
		t.Fatalf("want the lowest true version on the way out, got:\n%s", got)
	}
}
