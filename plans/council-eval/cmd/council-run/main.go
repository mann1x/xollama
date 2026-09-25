// Command council-run runs the council through each candidate against a real
// engine: one hard question (the full council) and one "Hello!" (the direct
// path) per runner, alternating runners so none inherits another's warm slot.
//
//	council-run -url http://127.0.0.1:38311 -doc doc.md -runs 1 -out results.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"councileval/council"
	"councileval/engine"
	"councileval/impl/baseline"
	"councileval/impl/eino"
	"councileval/impl/langgraphgo"
	"councileval/impl/trpcagent"
)

type row struct {
	Runner, Path, Route                    string
	WallS, FirstThinkingS, FirstContentS   float64
	ThinkingEvents, ContentEvents          int
	AnswerBytes                            int
	PromptTokens, CachedTokens, Completion int64
	GoroutinesBefore, GoroutinesAfter      int
	Queued                                 int64
	Leaked                                 []string
	CloseProblems                          []string
	Err                                    string
	AnswerHead                             string
}

func main() {
	url := flag.String("url", "http://127.0.0.1:38311", "engine")
	docPath := flag.String("doc", "", "document for the conversation")
	runs := flag.Int("runs", 1, "rounds over all runners")
	out := flag.String("out", "", "write rows as JSON here")
	only := flag.String("only", "", "comma-separated runner names")
	flag.Parse()
	doc, err := os.ReadFile(*docPath)
	if err != nil {
		panic(err)
	}
	runners := []council.Runner{baseline.Runner{}, &eino.Runner{}, &eino.Runner{Native: true},
		&langgraphgo.Runner{}, trpcagent.New(), &trpcagent.Runner{Native: true}}
	names := func(r council.Runner) string {
		n := r.Name()
		if v, ok := r.(*trpcagent.Runner); ok && v.Native && !strings.HasSuffix(n, "-native") {
			n += "-native"
		}
		return n
	}
	var rows []row
	for range *runs {
		for _, r := range runners {
			if *only != "" && !strings.Contains(","+*only+",", ","+names(r)+",") {
				continue
			}
			for _, q := range []string{"Review the design in the document: what are its three weakest points, what failure would each cause in production, and how would you fix each?", "Hello!"} {
				rows = append(rows, runOne(*url, names(r), r, string(doc), q))
				x := rows[len(rows)-1]
				fmt.Printf("%-16s %-7s route=%-7s wall=%6.2fs think1=%5.2fs content1=%5.2fs ev=%d/%d prompt=%d cached=%d gen=%d g=%d->%d q=%d leaked=%v closeproblems=%v %s\n",
					x.Runner, x.Path, x.Route, x.WallS, x.FirstThinkingS, x.FirstContentS, x.ThinkingEvents, x.ContentEvents,
					x.PromptTokens, x.CachedTokens, x.Completion, x.GoroutinesBefore, x.GoroutinesAfter, x.Queued, x.Leaked, x.CloseProblems, x.Err)
			}
		}
	}
	if *out != "" {
		b, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			panic(err)
		}
	}
}

// leaked lists this run's sessions the engine still holds.
func leaked(url, prefix string) []string {
	resp, err := http.Get(url + "/kv")
	if err != nil {
		return []string{"kv: " + err.Error()}
	}
	defer resp.Body.Close()
	var kv struct {
		Allocations []struct {
			SessionID string `json:"session_id"`
		} `json:"allocations"`
	}
	json.NewDecoder(resp.Body).Decode(&kv)
	var out []string
	for _, a := range kv.Allocations {
		if strings.HasPrefix(a.SessionID, prefix) {
			out = append(out, a.SessionID)
		}
	}
	return out
}

func runOne(url, name string, r council.Runner, doc, q string) row {
	nonce := fmt.Sprintf("%x", rand.Uint64())
	conv := []council.Message{
		{Role: "system", Content: council.Charter},
		{Role: "user", Content: "[ref " + nonce + "] Here is a design document I am working on:\n\n" + doc},
		{Role: "assistant", Content: "I have read the document. What would you like to know?"},
		{Role: "user", Content: q},
	}
	m := &engine.Model{URL: url, Session: "p1-" + name + "-" + nonce, NumCtx: 16384}
	var mu sync.Mutex
	var t1, c1 time.Duration
	var nt, nc int
	x := row{Runner: name, Path: map[bool]string{true: "direct", false: "council"}[q == "Hello!"], GoroutinesBefore: runtime.NumGoroutine()}
	t0 := time.Now()
	res, err := r.Run(context.Background(), council.Defaults(), m, conv, func(e council.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.Kind == council.Thinking {
			if nt == 0 {
				t1 = time.Since(t0)
			}
			nt++
		} else {
			if nc == 0 {
				c1 = time.Since(t0)
			}
			nc++
		}
	})
	x.WallS = time.Since(t0).Seconds()
	m.CloseAll()
	m.Closes.Range(func(k, v any) bool {
		if !strings.Contains(v.(string), `"released":true`) {
			x.CloseProblems = append(x.CloseProblems, k.(string)+" => "+v.(string))
		}
		return true
	})
	x.Leaked = leaked(url, m.Session)
	x.FirstThinkingS, x.FirstContentS = t1.Seconds(), c1.Seconds()
	x.ThinkingEvents, x.ContentEvents = nt, nc
	x.Route, x.AnswerBytes = res.Route, len(res.Answer)
	x.AnswerHead = res.Answer[:min(len(res.Answer), 300)]
	x.PromptTokens, x.CachedTokens, x.Completion = m.PromptTokens.Load(), m.CachedTokens.Load(), m.Completion.Load()
	x.Queued = m.Queued.Load()
	time.Sleep(200 * time.Millisecond)
	x.GoroutinesAfter = runtime.NumGoroutine()
	if err != nil {
		x.Err = err.Error()
	}
	return x
}
