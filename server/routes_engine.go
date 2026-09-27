package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/types/model"
)

// xollama-hook: engine-introspect
//
// /api/engine — the engine's own surface: read back what it is doing, and
// drive its pools, sessions and windows.
//
// Every fork setting is written into the engine's argv at launch and then
// vanishes. The engine answers for itself on its own HTTP surface, but that
// surface is on a private port ollama picks at random, so the only way to see
// it was to find the port in a log. This is that surface, through the API a
// user already has: every management route the engine has, by name from the
// table in llm/engine_introspect.go.
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
	Method   string `json:"method"`
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

// EngineHandler serves /api/engine: GET, POST and DELETE.
//
// With no model (GET only), it lists what is loaded, which engine is serving
// each, and the routes that may be called. With ?model= and ?endpoint=, it
// makes one call from that table against that model's engine, forwarding the
// request body and every other query parameter (?live=1, ?once=1).
func (s *Server) EngineHandler(c *gin.Context) {
	name := c.Query("model")
	method := c.Request.Method

	if name == "" {
		if method != http.MethodGet {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name the model whose engine to call with ?model="})
			return
		}
		loaded := s.sched.loadedModels()
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

	endpoint, err := llm.ValidEngineEndpoint(method, c.Query("endpoint"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	lm, ok := findLoadedModel(s.sched.loadedModels(), name)
	if !ok {
		// Deliberately distinct from "no such model": a model that exists but
		// is not loaded has no engine to ask, and saying so is the answer.
		c.JSON(http.StatusNotFound, gin.H{"error": "model \"" + name + "\" is not loaded; only a running model has an engine to call"})
		return
	}

	reader, ok := lm.llama.(llm.EngineIntrospector)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "this model is not served by an engine with an HTTP surface"})
		return
	}

	query := c.Request.URL.Query()
	query.Del("model")
	query.Del("endpoint")
	call := llm.EngineCall{Method: method, Endpoint: endpoint, Query: query}
	if method != http.MethodGet && c.Request.ContentLength != 0 {
		call.Body = http.MaxBytesReader(c.Writer, c.Request.Body, engineBodyLimit)
		call.ContentType = c.ContentType()
	}

	res, err := reader.EngineDo(c.Request.Context(), call)
	if err != nil {
		if errors.Is(err, llm.ErrUnknownIntrospectEndpoint) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer res.Body.Close()

	// A stream (/polykv/tps) is passed through as the engine sends it, event
	// by event: wrapping it would mean waiting for an end that never comes.
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		c.Status(res.StatusCode)
		c.Header("Content-Type", res.Header.Get("Content-Type"))
		c.Header("Cache-Control", "no-cache")
		buf := make([]byte, 32<<10)
		for {
			n, err := res.Body.Read(buf)
			if n > 0 {
				if _, werr := c.Writer.Write(buf[:n]); werr != nil {
					return
				}
				c.Writer.Flush()
			}
			if err != nil {
				return
			}
		}
	}

	// Bounded: /metrics on a long-lived server is the realistic large case
	// and is far below this.
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("%s %s on the engine: %v", method, endpoint, err)})
		return
	}

	out := engineReadResponse{
		Model:    engineDisplayName(lm),
		Engine:   reader.EngineName(),
		Method:   method,
		Endpoint: endpoint,
		Status:   res.StatusCode,
	}
	if json.Valid(body) {
		out.Body = json.RawMessage(body)
	} else {
		out.Text = string(body)
	}
	c.JSON(http.StatusOK, out)
}

// engineBodyLimit bounds a forwarded body. The largest real one is a pool
// built from tokens: a long conversation as a JSON array of ids.
const engineBodyLimit = 64 << 20

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
