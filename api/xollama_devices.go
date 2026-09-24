package api

import (
	"context"
	"net/http"
)

// XollamaDevicesPath lists the devices a model can be pinned to, as the
// server discovered them. `xollama tweak model` builds its device menu from
// it, so the menu offers what this server can actually run on rather than
// what the machine the CLI runs on happens to have.
const XollamaDevicesPath = "/api/xollama/devices"

// XollamaDevice is one device a model can be pinned to.
type XollamaDevice struct {
	// Backend is how a model config names it: "CUDA", "Vulkan", "ROCm".
	Backend string `json:"backend"`
	// ID is the backend's device index, as the serving engine numbers it.
	ID string `json:"id"`
	// PCIID is the stable identity to pin by, when discovery found one.
	PCIID       string `json:"pci_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Integrated  bool   `json:"integrated,omitempty"`
	TotalMemory uint64 `json:"total_memory"`
	FreeMemory  uint64 `json:"free_memory"`
	// Engine is the engine that serves this device: "opencoti" or "llamacpp".
	Engine string `json:"engine,omitempty"`
}

// XollamaDevicesResponse is what XollamaDevicesPath answers.
type XollamaDevicesResponse struct {
	Devices []XollamaDevice `json:"devices"`
	// Backends are the backends the installed payload carries, CPU included,
	// whether or not a device for them is present.
	Backends []string `json:"backends"`
}

// XollamaDevices lists the devices this server can pin a model to.
func (c *Client) XollamaDevices(ctx context.Context) (*XollamaDevicesResponse, error) {
	var resp XollamaDevicesResponse
	if err := c.do(ctx, http.MethodGet, XollamaDevicesPath, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
