package api

import (
	"context"
	"net/http"
)

// XollamaLinkProbePath measures each GPU's host link with the engine's own
// probe (`opencoti --link-probe`), for `xollama tweak server gpu`. Like the
// settings, only from the server's own machine.
const XollamaLinkProbePath = "/api/xollama/link-probe"

// LinkProbeResponse holds one result per backend probed.
type LinkProbeResponse struct {
	Results []LinkProbeResult `json:"results"`
}

// LinkProbeResult is one backend's probe, or why it was not run.
type LinkProbeResult struct {
	Backend string     `json:"backend"`
	Probe   *LinkProbe `json:"probe,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// LinkProbe is opencoti's "opencoti.link-probe/1" report.
type LinkProbe struct {
	Schema     string            `json:"schema"`
	ForcedGBps *float64          `json:"forced_gbps"`
	Presets    []LinkPreset      `json:"presets,omitempty"`
	Devices    []LinkProbeDevice `json:"devices"`
}

// LinkPreset is one PCIe generation and width, at the engine's own rate.
type LinkPreset struct {
	Label   string  `json:"label"`
	Gen     int     `json:"gen"`
	Width   int     `json:"width"`
	GBps    float64 `json:"gbps"`
	Current bool    `json:"current"`
}

// LinkProbeDevice is one GPU's link: as the bus reports it, and as measured.
type LinkProbeDevice struct {
	Index       int    `json:"index"`
	Backend     string `json:"backend"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PCIBusID    string `json:"pci_bus_id"`
	Link        struct {
		Detected     bool    `json:"detected"`
		Source       string  `json:"source"`
		Gen          int     `json:"gen"`
		Width        int     `json:"width"`
		GeometryGBps float64 `json:"geometry_gbps"`
		MaxGen       int     `json:"max_gen"`
		MaxWidth     int     `json:"max_width"`
	} `json:"link"`
	Measured struct {
		Available bool    `json:"available"`
		Method    string  `json:"method"`
		H2DGBps   float64 `json:"h2d_gbps"`
		D2HGBps   float64 `json:"d2h_gbps"`
	} `json:"measured"`
	WouldUseGBps float64 `json:"would_use_gbps"`
}

// LinkProbe asks the server to probe its GPUs' links.
func (c *Client) LinkProbe(ctx context.Context) (*LinkProbeResponse, error) {
	var resp LinkProbeResponse
	if err := c.do(ctx, http.MethodPost, XollamaLinkProbePath, struct{}{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
