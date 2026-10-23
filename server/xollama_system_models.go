package server

import (
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/model"
)

// systemModels lists the loaded models for /api/xollama/system, with the
// share of each GPU the server placed them on. A runner still loading holds
// its lock for the whole load, so it is skipped rather than waited for: the
// telemetry route never waits on a load.
func (s *Scheduler) systemModels(view []api.XollamaDevice) []api.SystemModel {
	s.loadedMu.Lock()
	runners := make([]*runnerRef, 0, len(s.loaded))
	for _, r := range s.loaded {
		runners = append(runners, r)
	}
	s.loadedMu.Unlock()

	out := []api.SystemModel{}
	for _, r := range runners {
		if !r.refMu.TryLock() {
			continue
		}
		rm := r.model.Load()
		if rm == nil || r.llama == nil {
			r.refMu.Unlock()
			continue
		}
		total, vram := r.llama.MemorySize()
		m := api.SystemModel{
			Name:     model.ParseName(rm.ShortName).DisplayShortest(),
			PID:      r.llama.Pid(),
			Size:     int64(total),
			SizeVRAM: int64(vram),
			Context:  r.llama.ContextLength(),
			Devices:  []api.SystemModelDevice{},
		}
		for _, g := range r.gpus {
			d := api.SystemModelDevice{Backend: g.Library, ID: g.ID, VRAM: r.llama.VRAMByGPU(g)}
			for _, k := range view {
				if strings.EqualFold(k.Backend, g.Library) && k.ID == g.ID {
					d.PCIID = k.PCIID
					break
				}
			}
			m.Devices = append(m.Devices, d)
		}
		r.refMu.Unlock()
		out = append(out, m)
	}
	return out
}
