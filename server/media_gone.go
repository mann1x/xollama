package server

import "log/slog"

// mediaEngineGone ends the keep-alive of a media engine whose process has
// exited, so the scheduler unloads it as its last request finishes. Left to
// its keep-alive, a crashed engine stayed in /api/ps as a loaded model, and
// its memory stayed booked against the GPU, until the timer ran out (c9's
// video engine on the RX 9070 XT, 2026-10-05). The caller holds refMu.
//
// Media engines only: a text runner that exited is found by the next request
// for it, as upstream does, and stock llama.cpp stays upstream's.
func mediaEngineGone(r *runnerRef) {
	if !isMediaKey(r.modelKey) || r.llama == nil || !r.llama.HasExited() || r.sessionDuration <= 0 {
		return
	}
	slog.Warn("media engine exited; unloading it now", "model", r.modelKey)
	r.sessionDuration = 0
}
