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

// The cost depends on how the engine runs the head on that host, which is
// not known before the load: on solidPC (RTX 3090, Linux) the draft context
// of the 26B-A4B head was 76 to 430 MiB from num_ctx 32768 to 256000 and
// num_batch 512 to 2048, on eleven2go (RTX 3090, Windows) 2338 MiB at 256000
// and 1024, and 4424 at 2048 (mail #2308). So the scheduler prices a head by
// the engine's own last report for it, where there is one.
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
