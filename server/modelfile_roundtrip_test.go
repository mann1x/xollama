package server

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/parser"
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

// A council is the config a derived Modelfile must not lose: dropped, the
// rebuilt model answers every turn with one call instead of a council. Its
// prompts are multi-line text with quotes, so this is also the test that the
// one-line XOLLAMA form carries text intact, through the whole chain a user
// drives: show --modelfile, then create.
func TestAModelfileCarriesACouncilThroughCreate(t *testing.T) {
	yes := true
	jitter := 0.0
	council := &xollama.Council{
		Enabled:           &yes,
		Charter:           "You are a \"council\".\nBe brief.",
		Researcher:        &xollama.CouncilRole{Count: 3, Prompt: "Dig into:\n- facts\n- sources"},
		TemperatureJitter: &jitter,
		Context:           &xollama.CouncilContext{Window: 32768, CompactAt: 0.85},
	}
	m := &Model{
		ModelPath: "/blobs/sha256-abc", Template: template.DefaultTemplate,
		Xollama: &xollama.Config{Version: 4, Engine: "opencoti", Council: council},
	}

	printed := m.String()
	mf, err := parser.ParseFile(strings.NewReader(printed))
	if err != nil {
		t.Fatalf("the printed Modelfile does not parse: %v\n%s", err, printed)
	}
	req, err := mf.CreateRequest(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := req.Xollama
	if got == nil || got.Council == nil {
		t.Fatalf("create lost the council:\n%s", printed)
	}
	if got.Version != 4 {
		t.Fatalf("the council came back as v%d, want 4", got.Version)
	}
	if got.Council.Charter != council.Charter || got.Council.Researcher.Prompt != council.Researcher.Prompt {
		t.Fatalf("text changed on the way:\ncharter %q\nprompt  %q", got.Council.Charter, got.Council.Researcher.Prompt)
	}
	if got.Council.TemperatureJitter == nil || *got.Council.TemperatureJitter != 0 {
		t.Fatal("temperature_jitter 0 was lost: it means no spread, and unstated means the default")
	}
	if got.Council.Researcher.Count != 3 || got.Council.Context.Window != 32768 || !got.Council.On() {
		t.Fatalf("council changed on the way: %+v", got.Council)
	}
}
