package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
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
		Xollama: true,
		Version: version.Version,
	})
}
