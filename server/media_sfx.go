package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm/engine"
	"github.com/ollama/ollama/types/xollama"
)

// hasSoundEffects is what the pins declare; a variable so a test can state an
// older pin.
var hasSoundEffects = engine.HasSoundEffects

// fillSFX fills a sound-effects request from the template.
func fillSFX(f mediaFields, s *xollama.SFXMedia) {
	d := s.Defaults
	if d == nil {
		return
	}
	fx := s.Fixed
	fill(f, fx, "seconds", "seconds", d.Seconds != nil, deref(d.Seconds))
	fill(f, fx, "negative_prompt", "negative_prompt", d.NegativePrompt != "", d.NegativePrompt)
	fill(f, fx, "steps", "steps", d.Steps > 0, d.Steps)
	fill(f, fx, "guidance_scale", "guidance_scale", d.GuidanceScale != nil, deref(d.GuidanceScale))
	fill(f, fx, "seed", "seed", d.Seed != nil, derefInt(d.Seed))
	fill(f, fx, "response_format", "response_format", d.ResponseFormat != "", d.ResponseFormat)
}

// SoundEffectsHandler serves POST /v1/audio/generations, opencoti's route: the
// reply is the audio itself, or JSON for response_format json, and the seed a
// client did not choose comes back in X-OC-Seed.
func (s *Server) SoundEffectsHandler(c *gin.Context) {
	if !hasSoundEffects() {
		mediaError(c, http.StatusNotImplemented, fmt.Sprintf("the engine this build ships lacks sound effects (%s, %s)", engine.FeatureAudioGenerations, engine.FeatureSFXSidecar))
		return
	}
	body, ok := readJSONBody(c)
	if !ok {
		return
	}
	name := mediaName(body.get("model"), CapabilitySoundEffects)
	body.set("model", name)
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	if media.SFX != nil {
		fillSFX(body, media.SFX)
	}
	data, err := json.Marshal(body)
	if err != nil {
		mediaError(c, http.StatusInternalServerError, err.Error())
		return
	}
	s.serveMedia(c, name, CapabilitySoundEffects, "sfx", "/v1/audio/generations", data, "application/json", nil)
}
