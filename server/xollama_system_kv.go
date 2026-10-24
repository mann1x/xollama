package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
)

// A model's KV report for /api/xollama/system (kv_host_telemetry_v1).
//
// The models are listed on every request, but the engine is not asked on
// every request: each runner's report is cached and refreshed in the
// background at most once per systemInterval, and only while someone polls.
// A request takes what is cached and never waits on the engine, so the first
// poll after a load reports null and the next one has the reading.

// kvFetchTimeout bounds one read of the engine's GET /kv. The engine answers
// it from its published snapshot; only before the first publish does it queue
// behind the engine's tasks, and then null is the honest answer.
const kvFetchTimeout = 2 * time.Second

type kvEntry struct {
	kv       *api.SystemModelKV
	at       time.Time
	fetching bool
	warned   bool
}

var (
	kvMu    sync.Mutex
	kvCache = map[*runnerRef]*kvEntry{}
	// kvNow and kvFetch are seams for tests.
	kvNow   = time.Now
	kvFetch = fetchEngineKV
)

// systemKV returns r's cached KV report and starts a refresh when it is due.
// The caller holds r.refMu. Only an opencoti runner is asked: stock
// llama.cpp has no /kv, so it is never called and its models report null.
func systemKV(r *runnerRef) *api.SystemModelKV {
	reader, ok := r.llama.(llm.EngineIntrospector)
	if !ok || reader.EngineName() != "opencoti" {
		return nil
	}

	kvMu.Lock()
	defer kvMu.Unlock()
	e := kvCache[r]
	if e == nil {
		e = &kvEntry{}
		kvCache[r] = e
	}
	if !e.fetching && kvNow().Sub(e.at) >= systemInterval {
		e.fetching = true
		go refreshKV(e, reader)
	}
	if e.kv == nil {
		return nil
	}
	kv := *e.kv
	return &kv
}

func refreshKV(e *kvEntry, reader llm.EngineIntrospector) {
	ctx, cancel := context.WithTimeout(context.Background(), kvFetchTimeout)
	defer cancel()
	kv, err := kvFetch(ctx, reader)
	at := kvNow()

	kvMu.Lock()
	defer kvMu.Unlock()
	e.fetching, e.at = false, at
	if err != nil {
		// A reading that cannot be taken is null, never the last one passed
		// off as current. Said once per runner: polls repeat every 2 s.
		e.kv = nil
		if !e.warned {
			e.warned = true
			slog.Warn("system telemetry: the engine's KV report could not be read", "error", err)
		}
		return
	}
	if kv != nil {
		kv.AsOf = at
	}
	e.kv = kv
}

// forgetKV drops the entries of runners that are no longer loaded.
func forgetKV(loaded map[*runnerRef]bool) {
	kvMu.Lock()
	defer kvMu.Unlock()
	for r := range kvCache {
		if !loaded[r] {
			delete(kvCache, r)
		}
	}
}

// engineRollingKV is the part of opencoti's rolling_kv report (patch 0481,
// in GET /kv and /props opencoti.rolling_kv) that /system passes on.
type engineRollingKV struct {
	Available     bool     `json:"available"`
	ResidencyMode string   `json:"residency_mode"`
	Engaged       bool     `json:"engaged"`
	KVarN         bool     `json:"kvarn"`
	Cells         uint64   `json:"cells"`
	WindowCells   uint64   `json:"window_cells"`
	TailCells     uint64   `json:"tail_cells"`
	WindowMiB     *float64 `json:"window_mib"`
	TailMiB       *float64 `json:"tail_mib"`
}

func fetchEngineKV(ctx context.Context, reader llm.EngineIntrospector) (*api.SystemModelKV, error) {
	res, err := reader.EngineDo(ctx, llm.EngineCall{Method: http.MethodGet, Endpoint: "kv"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /kv on the engine: %s", res.Status)
	}
	var body struct {
		RollingKV *engineRollingKV `json:"rolling_kv"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET /kv on the engine: %w", err)
	}
	if body.RollingKV == nil {
		return nil, errors.New("GET /kv on the engine: no rolling_kv report")
	}
	return kvFromEngine(*body.RollingKV), nil
}

// kvFromEngine turns the engine's report into /system's. The engine writes a
// size of 0 as null, and every size as null under KVarN, so a null size is
// only "unreadable" when the counts say a tail exists.
func kvFromEngine(r engineRollingKV) *api.SystemModelKV {
	if !r.Available {
		return nil
	}
	kv := &api.SystemModelKV{
		ResidencyMode: r.ResidencyMode,
		Engaged:       r.Engaged,
		KVarN:         r.KVarN,
		Cells:         r.Cells,
		WindowCells:   r.WindowCells,
		TailCells:     r.TailCells,
		DeviceBytes:   mibBytes(r.WindowMiB),
	}
	if !r.Engaged || r.TailCells == 0 {
		kv.HostBytes = ptrTo(uint64(0))
	} else {
		kv.HostBytes = mibBytes(r.TailMiB)
	}
	return kv
}

// mibBytes undoes the engine's bytes / 1048576.0: exact for any real size.
func mibBytes(mib *float64) *uint64 {
	if mib == nil || *mib < 0 {
		return nil
	}
	return ptrTo(uint64(math.Round(*mib * (1 << 20))))
}
