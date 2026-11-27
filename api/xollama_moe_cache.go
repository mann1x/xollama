package api

import (
	"context"
	"net/http"
)

// MoECacheDevice is the engine's record of the MoE expert cache on one device
// of a load (opencoti moe_cache_state_v1, GET /props opencoti.devices[]).
// An unknown value is null, never 0, as the engine sends it.
type MoECacheDevice struct {
	// Device is the engine's name of the device (CUDA0), PCI its address
	// (absent on Metal).
	Device string `json:"device"`
	PCI    string `json:"pci,omitempty"`
	// Verdict is pays, would_not_pay, probe_failed, multi_gpu, explicit or
	// off; Reason says why where the engine gives one.
	Verdict string  `json:"verdict"`
	Reason  *string `json:"reason,omitempty"`
	// CacheMiB is the cache on this device; HostExpertMiB the expert weights
	// kept in host memory (pinned unless mmap was given).
	CacheMiB      *int `json:"cache_mib,omitempty"`
	HostExpertMiB *int `json:"host_expert_mib,omitempty"`
	// LayersRAMFit is how many expert layers the fit left in host memory,
	// LayersRAMFinal how many after the cache took the VRAM they freed.
	LayersRAMFit   *int             `json:"layers_ram_fit,omitempty"`
	LayersRAMFinal *int             `json:"layers_ram_final,omitempty"`
	Admission      *bool            `json:"admission,omitempty"`
	MmapPageable   *bool            `json:"mmap_pageable,omitempty"`
	Inputs         *MoECacheInputs  `json:"inputs,omitempty"`
	Profile        *MoECacheProfile `json:"profile,omitempty"`
}

// MoECacheInputs are the measurements the automatic decision used, on the
// device it measured.
type MoECacheInputs struct {
	UploadMS         *float64 `json:"upload_ms,omitempty"`
	CPUMS            *float64 `json:"cpu_ms,omitempty"`
	CPUThreads       *int     `json:"cpu_threads,omitempty"`
	LinkForced       *bool    `json:"link_forced,omitempty"`
	RAMMSPerToken    *float64 `json:"ram_ms_per_token,omitempty"`
	CachedMSPerToken *float64 `json:"cached_ms_per_token,omitempty"`
}

// MoECacheProfile is the warm-start profile: the file the engine reads at
// load and writes at unload, whether it read one, and how many experts it
// uploaded from it.
type MoECacheProfile struct {
	Path        string `json:"path"`
	Read        bool   `json:"read"`
	WarmExperts *int   `json:"warm_experts,omitempty"`
}

// XollamaMoECacheForgetPath deletes a model's MoE cache warm-start profile.
const XollamaMoECacheForgetPath = "/api/xollama/moe-cache/forget"

// MoECacheForgetRequest names the model whose profile goes.
type MoECacheForgetRequest struct {
	Model string `json:"model"`
}

// MoECacheForgetResponse says what was removed. Removed is empty when the
// model had no profile; Unloaded when the model was loaded and had to be
// unloaded first (the engine writes the profile as it unloads).
type MoECacheForgetResponse struct {
	Key      string   `json:"key"`
	Removed  []string `json:"removed,omitempty"`
	Unloaded bool     `json:"unloaded,omitempty"`
	// Note is set when the operator's OPENCOTI_MOE_CACHE_PROFILE names a
	// file of its own, which is left alone.
	Note string `json:"note,omitempty"`
}

// ForgetMoECacheProfile asks the server to delete a model's warm-start
// profile, unloading the model first when it is loaded.
func (c *Client) ForgetMoECacheProfile(ctx context.Context, model string) (*MoECacheForgetResponse, error) {
	var resp MoECacheForgetResponse
	if err := c.do(ctx, http.MethodPost, XollamaMoECacheForgetPath, &MoECacheForgetRequest{Model: model}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
