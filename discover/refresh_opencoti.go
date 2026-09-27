package discover

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
)

// xollama-hook: opencoti-discover — the free-memory refresh.
//
// Upstream refreshes free memory before every load. When no runner is left to
// ask -- which is every model swap, because the old runner is gone by then --
// it starts llama-server once per library directory, all under one 3 s
// budget, and uses the old values if that runs out. On solidPC a stock
// llama-server discovery takes 16-20 s per directory (measured 2026-09-26,
// bug-117), so every refresh spent its full 3 s, three refreshes per swap
// cost about 9 s before the load began, and the free memory it was meant to
// refresh stayed stale anyway -- the 3090's too, whenever the Vulkan
// directory happened to come first in the map.
//
// On this fork the devices opencoti serves are refreshed by opencoti, the
// engine that will run on them and whose view of them discovery already
// takes at bootstrap (see overlayOpencotiDevices). A library directory whose
// devices are all refreshed is not started at all, and one whose refresh ran
// out of time is left alone for a while: its values would be stale either
// way, and waiting for them again only delays the load.
//
// Under XOLLAMA_ENGINE=llamacpp none of this runs: forkRefresh returns nil,
// and the refresh is upstream's, byte for byte. The directory that made it
// slow (the integrated Vulkan GPU's) is not in upstream's list in that mode
// anyway, since the igpu-vulkan hook admits it only when opencoti may serve.

// refreshCooldown is how long a library directory whose refresh ran out of
// time is skipped. Long enough that a burst of loads pays for it once, short
// enough that a host that got faster is noticed.
const refreshCooldown = 15 * time.Minute

// refreshSkippedUntil holds the directories in cooldown. Guarded by deviceMu,
// as every other piece of discovery state is.
var refreshSkippedUntil = map[string]time.Time{}

// refreshNow is the clock, so tests can move it.
var refreshNow = time.Now

// forkRefreshState carries one refresh's decisions. A nil *forkRefreshState is
// upstream's refresh: it skips nothing and remembers nothing.
type forkRefreshState struct{}

// forkRefresh refreshes, through opencoti, the devices it serves that are
// still waiting for a refresh, and marks them updated. It returns nil under
// XOLLAMA_ENGINE=llamacpp, where it runs nothing.
func forkRefresh(ctx context.Context, devices []ml.DeviceInfo, updated []bool) *forkRefreshState {
	selector := envconfig.Var(engine.EnvSelector)
	if strings.EqualFold(strings.TrimSpace(selector), string(engine.KindLlamaCpp)) {
		return nil
	}
	host := engine.Host()
	var backends []engine.Backend
	for _, b := range opencotiBackends {
		if engine.Enumerates(host, b, selector) && pendingIn(devices, updated, string(b)) {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return &forkRefreshState{}
	}
	artifact, err := opencotiArtifact()
	if err != nil {
		return &forkRefreshState{}
	}
	for _, b := range backends {
		key := "opencoti:" + string(b)
		if until, ok := refreshSkippedUntil[key]; ok && refreshNow().Before(until) {
			continue
		}
		start := time.Now()
		output, err := opencotiListDevices(ctx, artifact, b)
		listed := parseOpencotiDevices(output, string(b))
		if len(listed) == 0 {
			// No payload for this backend (b111 has no Vulkan), or no answer
			// in time: nothing to be authoritative with, and asking again on
			// every load would change neither.
			refreshSkippedUntil[key] = refreshNow().Add(refreshCooldown)
			slog.Debug("opencoti free-memory refresh found nothing; not asking again for a while",
				"backend", b, "duration", time.Since(start), "error", err, "retry_after", refreshCooldown)
			continue
		}
		n := 0
		for _, d := range mergeOpencotiBackend(slices.Clone(devices), string(b), listed, selector) {
			if d.Library != string(b) {
				continue
			}
			for i := range devices {
				if !updated[i] && sameRefreshDevice(d, devices[i]) {
					devices[i].FreeMemory = d.FreeMemory
					updated[i] = true
					n++
					break
				}
			}
		}
		slog.Debug("opencoti free-memory refresh", "backend", b, "refreshed", n, "duration", time.Since(start), "error", err)
	}
	return &forkRefreshState{}
}

func pendingIn(devices []ml.DeviceInfo, updated []bool, library string) bool {
	for i, d := range devices {
		if !updated[i] && d.Library == library {
			return true
		}
	}
	return false
}

// skip reports whether starting llama-server for dir would be wasted: no
// device there is still waiting, or dir is in cooldown.
func (f *forkRefreshState) skip(dir string, devices []ml.DeviceInfo, updated []bool) bool {
	if f == nil {
		return false
	}
	waiting := false
	for i, d := range devices {
		if !updated[i] && len(d.LibraryPath) > 0 && d.LibraryPath[len(d.LibraryPath)-1] == dir {
			waiting = true
			break
		}
	}
	if !waiting {
		return true
	}
	if until, ok := refreshSkippedUntil[dir]; ok {
		if refreshNow().Before(until) {
			return true
		}
		delete(refreshSkippedUntil, dir)
	}
	return false
}

// ran records how a directory's refresh went: one that ran out of time and
// found nothing goes into cooldown.
func (f *forkRefreshState) ran(dir string, budget context.Context, found int) {
	if f == nil || found > 0 || budget.Err() == nil {
		return
	}
	refreshSkippedUntil[dir] = refreshNow().Add(refreshCooldown)
	slog.Info("free-memory refresh through llama-server ran out of time; using the last values for this library for a while",
		"library_dir", dir, "retry_after", refreshCooldown)
}
