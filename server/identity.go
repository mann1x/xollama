package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/discover"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/version"
)

// XollamaIdentityHandler answers "are you xollama?".
//
// It exists so a client can tell this fork from a stock ollama on a port,
// which nothing upstream allows: /api/version answers on both, and its value
// is "0.0.0" on any build that was not linked with a version. A stock ollama
// has no such route and answers 404, and that difference is the whole test --
// see api.ResolveHost.
//
// Deliberately unauthenticated and free of side effects: it is a probe a client
// runs before it knows what it is talking to, so it must be safe to send to a
// server that turns out to be somebody else's.
func XollamaIdentityHandler(c *gin.Context) {
	c.JSON(http.StatusOK, api.XollamaIdentity{
		Xollama:  true,
		Version:  version.Version,
		Features: xollamaFeatures(),
	})
}

// xollamaFeatures is what this build serves, in the order the features shipped.
func xollamaFeatures() []string {
	return []string{api.FeatureCouncil, api.FeatureCouncilCompaction, api.FeatureCouncilTags, api.FeatureClientPlacement}
}

// XollamaDevicesHandler lists the devices a model can be pinned to.
//
// Every device discovery admitted, including an integrated GPU that unpinned
// models are kept off (see selectModelDevices): being pinnable is exactly
// what the listing is for.
func XollamaDevicesHandler(c *gin.Context) {
	c.JSON(http.StatusOK, xollamaDevices(discoverDevices(c.Request.Context(), nil)))
}

// discoverDevices is discovery behind a seam, so a route test does not run
// the engine.
var discoverDevices = discover.GPUDevices

func xollamaDevices(gpus []ml.DeviceInfo) api.XollamaDevicesResponse {
	resp := api.XollamaDevicesResponse{Devices: []api.XollamaDevice{}, Backends: builtBackends()}
	selector := envconfig.Var(engine.EnvSelector)
	for _, g := range gpus {
		kind := engine.Resolve(engine.Host(), []engine.Device{{
			Backend: engine.Backend(g.Library), ComputeMajor: g.ComputeMajor, ComputeMinor: g.ComputeMinor,
		}}, selector).Kind
		resp.Devices = append(resp.Devices, api.XollamaDevice{
			Backend:     g.Library,
			ID:          g.ID,
			PCIID:       g.PCIID,
			Name:        g.Name,
			Description: g.Description,
			Integrated:  g.Integrated,
			TotalMemory: g.TotalMemory,
			FreeMemory:  g.FreeMemory,
			Engine:      string(kind),
		})
	}
	return resp
}
