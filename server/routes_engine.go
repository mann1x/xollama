package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/model"
)

// xollama-hook: engine-introspect
//
// GET /api/engine — read back what the engine is actually doing.
//
// Every fork setting is written into the engine's argv at launch and then
// vanishes. The engine answers for itself on its own HTTP surface, but that
// surface is on a private port ollama picks at random, so the only way to see
// it was to find the port in a log. This is that surface, read-only, through
// the API a user already has.
//
// It matters most where the engine decides something for itself. A cache type
// can be accepted and still be served by a different tier; the engine's own
// /props is the only authority on which one. Nothing on this side knows.
//
// See docs/xollama/introspection.mdx.

// engineModelResponse is one loaded model, in the listing form.
type engineModelResponse struct {
	Model string `json:"model"`
	// Engine is which engine actually served this load, "opencoti" or
	// "llamacpp" — not which one was asked for. They differ whenever routing
	// declined a device, which is exactly when someone is asking.
	Engine string `json:"engine"`
	// Readable is false while the model is still loading, or for a runner with
	// no HTTP surface to read (the MLX path).
	Readable bool `json:"readable"`
}

// engineListResponse is GET /api/engine with no model named.
type engineListResponse struct {
	Models []engineModelResponse `json:"models"`
	// Endpoints is what may be asked for, so a caller is told rather than
	// having to guess and get a 400.
	Endpoints []string `json:"endpoints"`
}

// engineReadResponse is GET /api/engine?model=… — the engine's own answer,
// unaltered, with just enough around it to say who answered.
type engineReadResponse struct {
	Model    string `json:"model"`
	Engine   string `json:"engine"`
	Endpoint string `json:"endpoint"`
	// Status is the engine's own HTTP status. A 404 here is information: it is
	// how stock llama.cpp says it has no such feature, and flattening it into
	// an error would hide the clearest answer available.
	Status int `json:"status"`
	// Body is the engine's JSON when it sent JSON. Exactly as sent — nothing
	// here parses or renames the engine's fields, because a translation layer
	// over someone else's evolving schema goes stale silently.
	Body json.RawMessage `json:"body,omitempty"`
	// Text carries a body that is not JSON. /metrics is Prometheus text, and
	// an error body is usually plain.
	Text string `json:"text,omitempty"`
}

// EngineHandler serves GET /api/engine.
//
// With no model, it lists what is loaded and which engine is serving each.
// With ?model=, it reads one whitelisted endpoint from that model's engine.
func (s *Server) EngineHandler(c *gin.Context) {
	name := c.Query("model")

	endpoint, err := llm.ValidIntrospectEndpoint(c.Query("endpoint"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	loaded := s.sched.loadedModels()
	if name == "" {
		models := make([]engineModelResponse, 0, len(loaded))
		for _, lm := range loaded {
			engine, readable := engineFor(lm)
			models = append(models, engineModelResponse{
				Model:    engineDisplayName(lm),
				Engine:   engine,
				Readable: readable,
			})
		}
		c.JSON(http.StatusOK, engineListResponse{Models: models, Endpoints: llm.IntrospectEndpoints()})
		return
	}

	lm, ok := findLoadedModel(loaded, name)
	if !ok {
		// Deliberately distinct from "no such model": a model that exists but
		// is not loaded has no engine to ask, and saying so is the answer.
		c.JSON(http.StatusNotFound, gin.H{"error": "model \"" + name + "\" is not loaded; only a running model has an engine to read"})
		return
	}

	reader, ok := lm.llama.(llm.EngineIntrospector)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "this model is not served by an engine with a readable surface"})
		return
	}

	status, body, err := reader.EngineGet(c.Request.Context(), endpoint)
	if err != nil {
		if errors.Is(err, llm.ErrUnknownIntrospectEndpoint) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	res := engineReadResponse{
		Model:    engineDisplayName(lm),
		Engine:   reader.EngineName(),
		Endpoint: endpoint,
		Status:   status,
	}
	if json.Valid(body) {
		res.Body = json.RawMessage(body)
	} else {
		res.Text = string(body)
	}
	c.JSON(http.StatusOK, res)
}

// engineDisplayName renders a loaded model the way /api/ps does, so the name a
// caller reads there is the name this accepts.
func engineDisplayName(lm loadedModel) string {
	if lm.model == nil {
		return ""
	}
	return model.ParseName(lm.model.ShortName).DisplayShortest()
}

// engineFor names the engine serving a loaded model, and whether it can be read.
func engineFor(lm loadedModel) (string, bool) {
	if reader, ok := lm.llama.(llm.EngineIntrospector); ok {
		return reader.EngineName(), true
	}
	return "", false
}

// findLoadedModel matches a caller's name against what is loaded.
//
// Both spellings are accepted, because both are in circulation: the short name
// /api/ps prints, and whatever the caller typed at /api/generate. Requiring the
// canonical form would make this answerable only by someone who already knows
// the answer.
func findLoadedModel(loaded []loadedModel, name string) (loadedModel, bool) {
	want := model.ParseName(name)
	for _, lm := range loaded {
		if lm.model == nil || lm.llama == nil {
			continue
		}
		if lm.model.ShortName == name || engineDisplayName(lm) == name {
			return lm, true
		}
		if want.IsValid() && model.ParseName(lm.model.ShortName) == want {
			return lm, true
		}
	}
	return loadedModel{}, false
}
