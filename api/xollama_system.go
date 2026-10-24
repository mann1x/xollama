package api

import (
	"context"
	"net/http"
	"time"
)

// XollamaSystemPath is a live view of the machine the server runs on: the
// host, the server's own processes, every GPU the vendor's driver reports,
// and the loaded models. It is one cached snapshot the server refreshes on its
// own timer while someone polls it, so a monitor polling every few seconds
// costs the server nothing it would notice; a request never starts discovery,
// an engine or a probe.
const XollamaSystemPath = "/api/xollama/system"

// FeatureSystemTelemetry: XollamaSystemPath is served.
const FeatureSystemTelemetry = "system_telemetry_v1"

// FeatureKVHostTelemetry: each model in XollamaSystemPath carries KV, the
// engine's own report of where its KV cache lives.
const FeatureKVHostTelemetry = "kv_host_telemetry_v1"

// A nil pointer field below is a value this platform cannot read cheaply, or
// a rate that needs a second snapshot: never an estimate, and never 0 in its
// place.

// SystemResponse is what XollamaSystemPath answers.
type SystemResponse struct {
	// AsOf is when the snapshot was taken.
	AsOf time.Time `json:"as_of"`
	// IntervalMS is how often the snapshot is refreshed while it is polled.
	IntervalMS int            `json:"interval_ms"`
	Host       SystemHost     `json:"host"`
	Server     SystemServer   `json:"server"`
	Devices    []SystemDevice `json:"devices"`
	Models     []SystemModel  `json:"models"`
}

// SystemHost is the machine as a whole.
type SystemHost struct {
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Hostname string   `json:"hostname,omitempty"`
	UptimeS  *uint64  `json:"uptime_s"`
	CPUPct   *float64 `json:"cpu_pct"`
	CPUCount int      `json:"cpu_count"`
	// Load is the 1, 5 and 15 minute load average; null on Windows.
	Load      []float64 `json:"load"`
	MemTotal  *uint64   `json:"mem_total"`
	MemUsed   *uint64   `json:"mem_used"`
	SwapTotal *uint64   `json:"swap_total"`
	SwapUsed  *uint64   `json:"swap_used"`
}

// SystemServer is this server's process and every process under it: runners,
// engines and their helpers.
type SystemServer struct {
	PID int `json:"pid"`
	// CPUPct and RSS add up the whole tree. CPUPct is of one core, so a tree
	// keeping four cores busy reads 400.
	CPUPct   *float64        `json:"cpu_pct"`
	RSS      *uint64         `json:"rss"`
	Procs    int             `json:"procs"`
	Children []SystemProcess `json:"children"`
}

// SystemProcess is one process under the server.
type SystemProcess struct {
	PID    int      `json:"pid"`
	Name   string   `json:"name"`
	Model  string   `json:"model,omitempty"`
	CPUPct *float64 `json:"cpu_pct"`
	RSS    *uint64  `json:"rss"`
}

// SystemDevice is one GPU as its vendor's driver reports it.
type SystemDevice struct {
	// PCIID is the bus address, as /api/xollama/devices writes it.
	PCIID string `json:"pci_id,omitempty"`
	// Vendor is "nvidia", "amd", "intel" or "apple".
	Vendor string `json:"vendor"`
	Name   string `json:"name,omitempty"`
	// Source is where the readings come from: "nvml", "sysfs", "d3dkmt" or
	// "iokit".
	Source string `json:"source"`
	// Backend, ID and Engine are the server's own view of this GPU, from
	// its last discovery, when it uses the GPU; see /api/xollama/devices.
	Backend     string      `json:"backend,omitempty"`
	ID          string      `json:"id,omitempty"`
	Engine      string      `json:"engine,omitempty"`
	Integrated  bool        `json:"integrated,omitempty"`
	TotalMemory *uint64     `json:"total_memory"`
	FreeMemory  *uint64     `json:"free_memory"`
	UsedMemory  *uint64     `json:"used_memory"`
	UtilPct     *float64    `json:"util_pct"`
	MemUtilPct  *float64    `json:"mem_util_pct"`
	TempC       *float64    `json:"temp_c"`
	PowerW      *float64    `json:"power_w"`
	PowerLimitW *float64    `json:"power_limit_w"`
	ClockMHz    *uint64     `json:"clock_mhz"`
	ClockMaxMHz *uint64     `json:"clock_max_mhz"`
	FanPct      *float64    `json:"fan_pct"`
	Link        *SystemLink `json:"link"`
}

// SystemLink is the GPU's host link as the bus reports it, plus the last
// measurement of it, when a link probe ran since the server started. A poll
// never measures.
type SystemLink struct {
	Gen      *int     `json:"gen"`
	Width    *int     `json:"width"`
	MaxGen   *int     `json:"max_gen"`
	MaxWidth *int     `json:"max_width"`
	H2DGBps  *float64 `json:"h2d_gbps"`
	D2HGBps  *float64 `json:"d2h_gbps"`
	// MeasuredAt is when H2DGBps and D2HGBps were measured.
	MeasuredAt *time.Time `json:"measured_at,omitempty"`
}

// SystemModel is a loaded model as the server placed it.
type SystemModel struct {
	Name     string `json:"name"`
	PID      int    `json:"pid,omitempty"`
	Size     int64  `json:"size"`
	SizeVRAM int64  `json:"size_vram"`
	// Devices are the GPUs the model was placed on, with its share of each.
	Devices []SystemModelDevice `json:"devices"`
	Context int                 `json:"context_length,omitempty"`
	// KV is null for a model whose engine does not report it (stock
	// llama.cpp, an engine asleep) and until its first reading arrives.
	KV *SystemModelKV `json:"kv"`
}

// SystemModelKV is opencoti's report of a model's KV cache: how much of it is
// resident on the GPU and how much is the rolling window's tail in pinned host
// memory (kv.rolling_window, --kv-rolling-window). It is read from the engine's
// GET /kv in the background and is as old as AsOf says.
type SystemModelKV struct {
	AsOf time.Time `json:"as_of"`
	// ResidencyMode is the configured tactic: "auto", "head" or "window".
	ResidencyMode string `json:"residency_mode"`
	// Engaged is true while part of the cache streams from the host.
	Engaged bool `json:"engaged"`
	// KVarN caches report cells but no byte sizes.
	KVarN       bool   `json:"kvarn"`
	Cells       uint64 `json:"cells"`
	WindowCells uint64 `json:"window_cells"`
	TailCells   uint64 `json:"tail_cells"`
	// DeviceBytes is the resident window on the GPU; null when the engine
	// reports no size for it.
	DeviceBytes *uint64 `json:"device_bytes"`
	// HostBytes is the tail held in pinned host memory, allocated at load: 0
	// when no window is engaged, null when the engine reports no size (KVarN).
	HostBytes *uint64 `json:"host_bytes"`
}

// SystemModelDevice is a model's share of one GPU.
type SystemModelDevice struct {
	Backend string `json:"backend"`
	ID      string `json:"id"`
	PCIID   string `json:"pci_id,omitempty"`
	VRAM    uint64 `json:"vram"`
}

// System reads the server's system snapshot.
func (c *Client) System(ctx context.Context) (*SystemResponse, error) {
	var resp SystemResponse
	if err := c.do(ctx, http.MethodGet, XollamaSystemPath, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
