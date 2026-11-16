package discover

import (
	"log/slog"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/ml"
)

// listedFree is, per device, the free memory a listing last measured: the
// bootstrap, or a refresh through the engine or llama-server. A runner's
// own accounting (GetDeviceInfos) is not a listing: it is "free at load
// minus what this runner uses", true only while that runner is loaded.
// Guarded by deviceMu, as every other piece of discovery state is.
var listedFree = map[string]uint64{}

func listedFreeKey(d ml.DeviceInfo) string {
	return d.Library + "|" + strings.ToLower(d.PCIID) + "|" + d.ID
}

// noteListedFree records the devices a listing measured; mask nil means all.
func noteListedFree(devices []ml.DeviceInfo, mask []bool) {
	for i, d := range devices {
		if mask == nil || (i < len(mask) && mask[i]) {
			listedFree[listedFreeKey(d)] = d.FreeMemory
		}
	}
}

// settleStaleFree ends a refresh. Devices a listing measured are recorded.
// A device nothing measured, and no loaded runner reported, still carries
// whatever was written last, which can be a runner's accounting from a
// runner that is gone. On eleven2go that was a 0 (mails #2309/#2310): it
// stayed in the table after the runner unloaded, every refresh that could
// not reach the card kept it, and every load went to another GPU until the
// server was restarted. Such a device goes back to its last listed value.
// XOLLAMA_ENGINE=llamacpp keeps upstream's table (CLAUDE.md rule 2).
func settleStaleFree(devices []ml.DeviceInfo, updated, byRunner []bool) {
	if strings.EqualFold(strings.TrimSpace(envconfig.Engine()), "llamacpp") {
		return
	}
	listed := make([]bool, len(updated))
	for i := range updated {
		listed[i] = updated[i] && (i >= len(byRunner) || !byRunner[i])
	}
	noteListedFree(devices, listed)
	for i := range devices {
		if updated[i] {
			continue
		}
		last, ok := listedFree[listedFreeKey(devices[i])]
		if !ok || last == devices[i].FreeMemory {
			continue
		}
		slog.Warn("free memory of a device no refresh reached goes back to its last listed value; the value it had came from a runner that is no longer loaded",
			"library", devices[i].Library, "id", devices[i].ID, "pci_id", devices[i].PCIID,
			"stale", format.HumanBytes2(devices[i].FreeMemory), "listed", format.HumanBytes2(last))
		devices[i].FreeMemory = last
	}
}
