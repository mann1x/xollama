package create

import (
	"fmt"

	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/xollama"
)

// syncMediaLayers makes the model's media layers exactly the components its
// config names (plans/media-integration.md). It runs only when a request
// states the config -- the same pointer rule as the config layer -- so a
// child created FROM a parent without a config keeps the parent's media
// along with the config that names it.
//
// Each component must already be a blob in the store: the client uploads it
// (/api/blobs) or a pull brought it. A missing blob is refused rather than
// written as a layer nothing can fetch.
//
// xollama-hook: model-config (called from ApplyModelfileLayers)
func syncMediaLayers(layers []manifest.Layer, media *xollama.Media) ([]manifest.Layer, error) {
	layers = removeLayersByMediaType(layers, xollama.MediaTypeMedia)
	for _, c := range media.Components() {
		layer, err := manifest.NewLayerFromLayer(c.Digest, xollama.MediaTypeMedia, "")
		if err != nil {
			return nil, fmt.Errorf("media component %s (%s) is not in the model store; upload it first: %w", c.Name, c.Digest, err)
		}
		layer.Name = c.Name
		layers = append(layers, layer)
	}
	return layers, nil
}
