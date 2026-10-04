package server

import (
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/model"
)

// The operator's default media model per kind, XOLLAMA_MEDIA_DEFAULTS. A
// server with two image models has no single answer for a client that asks
// for "an image model"; this is the operator's, marked on /v1/models as
// default_for and used by a media request that names no model.

// mediaKindNames maps the kinds an operator may write to the capabilities
// they stand for. "image" is both image routes.
var mediaKindNames = map[string][]model.Capability{
	"image":            {CapabilityImageGeneration, CapabilityImageEdit},
	"image_generation": {CapabilityImageGeneration},
	"image_edit":       {CapabilityImageEdit},
	"transcription":    {CapabilityTranscription},
	"stt":              {CapabilityTranscription},
	"speech":           {CapabilitySpeech},
	"tts":              {CapabilitySpeech},
	"video":            {CapabilityVideo},
}

// mediaDefaults reads kind=model pairs. A later pair for the same kind wins,
// so "image=a,image_edit=b" edits with b. Unknown kinds and invalid names are
// skipped.
func mediaDefaults() map[model.Capability]model.Name {
	out := map[model.Capability]model.Name{}
	for _, part := range strings.Split(envconfig.MediaDefaults(), ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n := model.ParseName(strings.TrimSpace(v))
		if !n.IsValid() {
			continue
		}
		for _, kind := range mediaKindNames[strings.ToLower(strings.TrimSpace(k))] {
			out[kind] = n
		}
	}
	return out
}

// mediaDefaultFor lists the kinds the model named id is the default for,
// among those it serves: a default naming a model without that kind is not
// one.
func mediaDefaultFor(id string, caps []model.Capability) []model.Capability {
	defaults := mediaDefaults()
	if len(defaults) == 0 {
		return nil
	}
	n := model.ParseName(id)
	var out []model.Capability
	for _, c := range caps {
		if d, ok := defaults[c]; ok && d.EqualFold(n) {
			out = append(out, c)
		}
	}
	return out
}

// mediaName is the model a request names, else the default for its kind.
func mediaName(name string, kind model.Capability) string {
	if name != "" {
		return name
	}
	if d, ok := mediaDefaults()[kind]; ok {
		return d.DisplayShortest()
	}
	return ""
}
