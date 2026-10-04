package llm

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

// A schema conversion is an empty completion. On opencoti it states a small
// window of its own, so it is not booked a whole default one; on stock
// llama.cpp the body is upstream's. Either way the result is remembered.
func TestASchemaConversionStatesItsWindowOnOpencotiOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opencoti bool
		schema   string
		want     float64
		stated   bool
	}{
		{"opencoti", true, `{"type":"object","properties":{"grammar_test_a":{"type":"string"}}}`, GrammarWindow, true},
		{"stock llama.cpp", false, `{"type":"object","properties":{"grammar_test_b":{"type":"string"}}}`, 0, false},
	} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{"generation_settings/grammar":"root ::= \"x\""}`))
		}))
		s := &llamaServerRunner{port: srv.Listener.Addr().(*net.TCPAddr).Port, client: srv.Client(), usedOpencoti: tc.opencoti, cmd: &exec.Cmd{}}
		schema := json.RawMessage(tc.schema)
		if SchemaGrammarKnown(schema) {
			t.Fatalf("%s: the schema is known before it was converted", tc.name)
		}
		if _, err := s.schemaGrammar(t.Context(), schema); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		srv.Close()
		got, stated := body["num_ctx"]
		if stated != tc.stated || (stated && got != tc.want) {
			t.Errorf("%s: num_ctx = %v (stated %v), want %v (stated %v)", tc.name, got, stated, tc.want, tc.stated)
		}
		if !SchemaGrammarKnown(schema) {
			t.Errorf("%s: the converted schema is not remembered", tc.name)
		}
	}
}
