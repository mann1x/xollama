package llm

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// DraftHeadMeasure is what the engine reported an assistant head's draft
// context took on a load: "[spec] assistant head took N MiB on CUDA0
// (weights W + draft context C) against 824 MiB booked".
type DraftHeadMeasure struct {
	NumCtx, NumBatch int
	Context          uint64 // bytes
}

// The cost depends on the engine and the host: under c14 r15 the draft
// context of the 26B-A4B head was built at the target's batch, 76 to 430 MiB
// on solidPC (RTX 3090, Linux) and 2338 / 4424 MiB on eleven2go (RTX 3090,
// Windows, num_batch 1024 / 2048, mail #2308); since c14 r16 it has
// max(32, drafting sequences) rows, 6 MiB at num_ctx 32768 and 20 at 256000
// on solidPC (our #2311). So the scheduler prices a head by the engine's own
// last report for it, where there is one.
var draftHeadRegex = regexp.MustCompile(`assistant head took (\d+) MiB on \S+ \(weights (\d+) \+ draft context (\d+)\)`)

var draftHeads sync.Map // drafter blob name -> DraftHeadMeasure

// blobName keys a drafter by its file name (the blob's digest): the engine
// prints the path as it opened it, which on Windows need not be spelled as
// xOllama passed it.
func blobName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// noteDraftHead records the report, keyed by the file the engine last
// opened, which for this line is the drafter's.
func (w *memoryParsingWriter) noteDraftHead(line []byte) {
	m := draftHeadRegex.FindSubmatch(line)
	if m == nil || w.model == "" || w.runner == nil {
		return
	}
	ctxMiB, err := strconv.ParseUint(string(m[3]), 10, 64)
	if err != nil {
		return
	}
	StoreDraftHeadContext(w.model, DraftHeadMeasure{
		NumCtx:   w.runner.options.NumCtx,
		NumBatch: w.runner.options.NumBatch,
		Context:  ctxMiB << 20,
	})
}

// StoreDraftHeadContext records a report for the drafter at path.
func StoreDraftHeadContext(path string, m DraftHeadMeasure) {
	draftHeads.Store(blobName(path), m)
}

// DraftHeadContext is the engine's last report for the drafter at path.
func DraftHeadContext(path string) (DraftHeadMeasure, bool) {
	v, ok := draftHeads.Load(blobName(path))
	if !ok {
		return DraftHeadMeasure{}, false
	}
	return v.(DraftHeadMeasure), true
}
