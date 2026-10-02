package api

import (
	"context"
	"net/http"

	"github.com/ollama/ollama/types/xollama"
)

// XollamaSettingsPath reads and changes the server's own settings
// (plans/system-settings.md). Like the API key, the server answers it only
// from its own machine, never through a proxy, and with the current key
// when one is set: these are the host's settings, and their values can
// include proxies and paths.
const XollamaSettingsPath = "/api/xollama/settings"

// SettingsRequest changes the settings. Envs maps a variable to its new
// override; a nil value removes the override, so the environment applies
// again. A variable not named is left as it is. A request with no changes
// only reads.
//
// Defaults, when set, replaces the server's defaults for the models'
// settings whole; an empty config clears them. nil leaves them as they are.
//
// GPU, when set, replaces the server's GPU policy whole; an empty one clears
// it.
type SettingsRequest struct {
	Envs     map[string]*string   `json:"envs,omitempty"`
	Defaults *xollama.Config      `json:"defaults,omitempty"`
	GPU      *xollama.GPUSettings `json:"gpu,omitempty"`
}

// SettingsEnv is one variable as the server sees it.
type SettingsEnv struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Value is what the server uses: the tweak file's value when it sets
	// one, else the environment's.
	Value string `json:"value,omitempty"`
	// Source is "tweak", "env" or "" (neither: the built-in default).
	Source string `json:"source,omitempty"`
	// Tweak and Env are both candidates, so an environment value the tweak
	// file overrides is still shown.
	Tweak    *string `json:"tweak,omitempty"`
	Env      *string `json:"env,omitempty"`
	Restart  bool    `json:"restart,omitempty"`
	External bool    `json:"external,omitempty"`
}

// SettingsResponse is every variable the server knows, plus any the tweak
// file sets for the engines, sorted by name. Restart names the variables
// this request changed that wait for the server to start again.
//
// Defaults are the server's defaults for every model that does not state a
// setting itself (plans/system-settings.md), nil when there are none.
type SettingsResponse struct {
	Path     string               `json:"path"`
	Envs     []SettingsEnv        `json:"envs"`
	Defaults *xollama.Config      `json:"defaults,omitempty"`
	GPU      *xollama.GPUSettings `json:"gpu,omitempty"`
	Restart  []string             `json:"restart,omitempty"`
}

// Settings sends req to the server's XollamaSettingsPath.
func (c *Client) Settings(ctx context.Context, req *SettingsRequest) (*SettingsResponse, error) {
	if req == nil {
		req = &SettingsRequest{}
	}
	var resp SettingsResponse
	if err := c.do(ctx, http.MethodPost, XollamaSettingsPath, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
