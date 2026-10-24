package systel

// isIntegratedUMA reports whether an adapter is an integrated GPU whose real
// memory is the shared system pool rather than a dedicated board. It uses the
// same thresholds as discover/amd.go (maxIntegratedVRAM / minSharedGTT) so
// /api/xollama/system and /api/xollama/devices classify a device the same way,
// and so the memory totals the two endpoints report agree for an iGPU.
//
// dedicated is the adapter's dedicated video memory (a dGPU's board VRAM, an
// APU's small UMA carve-out); shared is the system memory the GPU may address.
func isIntegratedUMA(dedicated, shared uint64) bool {
	const (
		maxIntegratedVRAM = 4 << 30 // an APU's UMA carve-out; a dGPU's board is larger
		minSharedSystem   = 8 << 30
	)
	return dedicated > 0 && dedicated <= maxIntegratedVRAM &&
		shared >= minSharedSystem && shared >= 4*dedicated
}
