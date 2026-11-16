package llm

import (
	"io"
	"testing"

	"github.com/ollama/ollama/api"
)

// The engine's report of the head, as eleven2go printed it (mail #2308), is
// recorded for the drafter file the engine opened last.
func TestTheEnginesHeadReportIsRecorded(t *testing.T) {
	runner := &llamaServerRunner{
		vramByDevice:     make(map[string]uint64),
		systemFreeAtLoad: make(map[string]uint64),
		options:          api.Options{Runner: api.Runner{NumCtx: 256000, NumBatch: 2048}},
	}
	w := &memoryParsingWriter{inner: io.Discard, runner: runner}
	log := `llama_model_loader: loaded meta data with 52 key-value pairs and 49 tensors from C:\Users\x\.ollama\models\blobs\sha256-5963a2ed (version GGUF V3 (latest))
srv    load_model: [spec] assistant head took 4850 MiB on CUDA0 (weights 426 + draft context 4424) against 824 MiB booked
`
	if _, err := w.Write([]byte(log)); err != nil {
		t.Fatal(err)
	}
	m, ok := DraftHeadContext("/elsewhere/blobs/sha256-5963a2ed")
	if !ok {
		t.Fatal("the head's report was not recorded for its blob")
	}
	if m.Context != 4424<<20 || m.NumCtx != 256000 || m.NumBatch != 2048 {
		t.Fatalf("recorded %+v", m)
	}
}
