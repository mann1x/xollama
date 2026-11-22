package llm

// Overcommitted is how many bytes of this runner's device buffers went past
// what each GPU had free when the engine started allocating (its first
// "using device <dev> (...) - N MiB free" line, systemFreeAtLoad).
//
// It is not 0 only when the engine was told to place more than the card
// holds and did: a model whose num_gpu reaches every layer is launched with
// -ngl, and the engine's fit then declines to move anything ("n_gpu_layers
// already set by user to 99, abort"). On Windows the driver lets the
// allocation pass the card and pages the rest into shared system memory
// (eleven2go, RX 9070 XT Vulkan, 2026-11-21, mail #2362: 16312 MiB projected
// against 14986 MiB free, about 3 tok/s), and MemorySize still read every
// device buffer as VRAM, so /api/ps showed the model 100% on the GPU.
//
// An integrated GPU is left out: its "free" is a heap of the host's memory,
// not a card that can be passed. Stock llama.cpp (XOLLAMA_ENGINE=llamacpp or
// a load opencoti does not serve) reports upstream's figures (CLAUDE.md
// rule 2).
func (s *llamaServerRunner) Overcommitted() uint64 {
	if !s.usedOpencoti {
		return 0
	}
	s.memoryMu.RLock()
	defer s.memoryMu.RUnlock()
	var over uint64
	for _, g := range s.gpus {
		if g.Integrated {
			continue
		}
		key := s.engineKey(g)
		free, ok := s.systemFreeAtLoad[key]
		if !ok {
			continue
		}
		if used := s.vramByDevice[key]; used > free {
			over += used - free
		}
	}
	return over
}

// residentVRAM is the VRAM a runner reports once what it overcommitted is
// taken off: memory paged out of the card is not on it.
func (s *llamaServerRunner) residentVRAM(vram uint64) uint64 {
	return vram - min(vram, s.Overcommitted())
}
