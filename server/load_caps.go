package server

import (
	"slices"
	"sort"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// A cap per kind of model, apart from OLLAMA_MAX_LOADED_MODELS. Counted in that
// one pool, a speech engine or a decision model took an LLM's place, and a load
// at the limit unloaded whatever had the shortest keep-alive, of any kind.
//
// One model's media is one engine, however many kinds it serves, so an engine
// counts once under each kind it serves and a load needs room under all of
// them. A model stays in the shared pool while any of its kinds has a cap of 0.
// These caps count runners: a load that does not fit in memory can still unload
// anything on its GPUs.
type loadKind struct {
	limit  func() uint
	serves func(*Model) bool
}

func mediaServes(serves func(*xollama.Media) bool) func(*Model) bool {
	return func(m *Model) bool {
		// Only the twin: an LLM that also has media keeps its place in the pool.
		if !isMediaKey(m.ModelPath) {
			return false
		}
		media := modelMedia(m)
		return media != nil && serves(media)
	}
}

// decisionLimit is 0 with XOLLAMA_ENGINE=llamacpp: unlike media, a decision
// model runs on stock llama.cpp, where off means upstream's one pool.
func decisionLimit() uint {
	if envconfig.Engine() == xollama.EngineLlamaCpp {
		return 0
	}
	return envconfig.MaxLoadedDecisionModels()
}

// isDecisionModel is what Capabilities says, from the fields it reads: this
// runs for every loaded model on every scheduling pass. The Config entry is an
// MLX decision model's, which has no GGUF metadata.
func isDecisionModel(m *Model) bool {
	return m.metadata.String("decision.type") != "" || slices.Contains(m.Config.Capabilities, string(model.CapabilityDecision))
}

var loadKinds = []loadKind{
	{envconfig.MaxLoadedImageModels, mediaServes(func(m *xollama.Media) bool { return m.Image != nil })},
	{envconfig.MaxLoadedSpeechModels, mediaServes(func(m *xollama.Media) bool { return m.TTS != nil })},
	{envconfig.MaxLoadedTranscriptionModels, mediaServes(func(m *xollama.Media) bool { return m.STT != nil })},
	{envconfig.MaxLoadedVideoModels, mediaServes(func(m *xollama.Media) bool { return m.Video != nil })},
	{decisionLimit, isDecisionModel},
}

// kindsOf lists the capped kinds a model is, none for a plain model.
func kindsOf(m *Model) []loadKind {
	if m == nil {
		return nil
	}
	var out []loadKind
	for _, k := range loadKinds {
		if k.serves(m) {
			out = append(out, k)
		}
	}
	return out
}

// inSharedPool says whether a model counts against OLLAMA_MAX_LOADED_MODELS.
func inSharedPool(m *Model) bool {
	kinds := kindsOf(m)
	for _, k := range kinds {
		if k.limit() == 0 {
			return true
		}
	}
	return len(kinds) == 0
}

func (s *Scheduler) loadedRunners(keep func(*Model) bool) []*runnerRef {
	s.loadedMu.Lock()
	defer s.loadedMu.Unlock()
	out := make([]*runnerRef, 0, len(s.loaded))
	for _, r := range s.loaded {
		if keep(r.model.Load()) {
			out = append(out, r)
		}
	}
	return out
}

// pickRunnerToUnload is findRunnerToUnload's choice among the given runners.
func pickRunnerToUnload(runners []*runnerRef) *runnerRef {
	if len(runners) == 0 {
		return nil
	}
	sort.Sort(ByDurationAndName(runners))
	for _, r := range runners {
		r.refMu.Lock()
		rc := r.refCount
		r.refMu.Unlock()
		if rc == 0 {
			return r
		}
	}
	return runners[0]
}

// kindCapVictim returns a runner to unload so that m stays within the cap of
// every kind it is, or nil when there is room.
//
// xollama-hook: media (called from Scheduler.processPending)
func (s *Scheduler) kindCapVictim(m *Model) *runnerRef {
	for _, k := range kindsOf(m) {
		limit := k.limit()
		if limit == 0 {
			continue
		}
		same := s.loadedRunners(func(loaded *Model) bool {
			return loaded != nil && k.serves(loaded)
		})
		if uint(len(same)) >= limit {
			return pickRunnerToUnload(same)
		}
	}
	return nil
}

// sharedRunnerCount is what m's load is measured against
// OLLAMA_MAX_LOADED_MODELS with: 0 for a model outside the shared pool.
//
// xollama-hook: media (called from Scheduler.processPending)
func (s *Scheduler) sharedRunnerCount(m *Model) int {
	if !inSharedPool(m) {
		return 0
	}
	return len(s.loadedRunners(inSharedPool))
}

// findSharedRunnerToUnload is findRunnerToUnload within the shared pool: a
// runner outside it frees no place there.
//
// xollama-hook: media (called from Scheduler.processPending)
func (s *Scheduler) findSharedRunnerToUnload() *runnerRef {
	return pickRunnerToUnload(s.loadedRunners(inSharedPool))
}
