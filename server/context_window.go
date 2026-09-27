package server

import (
	"math"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/llm"
)

// exposeContextWindow passes the engine's X-Context-Window on to the client
// (docs/xollama/sessions.mdx): the request's context carries a collector the
// runner fills on admission, and the response writer sets the header just
// before the first byte goes out. The writer is only ever driven from the
// handler's goroutine, and the runner's report happens before the chunk that
// triggers the first write is handed over, so nothing races the header map.
//
// Without an engine that states a window -- stock llama.cpp, an opencoti
// without elastic admission -- nothing is reported and the response is
// upstream's, byte for byte.
func exposeContextWindow(c *gin.Context) {
	ctx, w := llm.WithContextWindow(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	c.Writer = &windowWriter{ResponseWriter: c.Writer, window: w}
}

type windowWriter struct {
	gin.ResponseWriter
	window *llm.ContextWindow
}

// apply sets the header while it can still be sent.
func (w *windowWriter) apply() {
	if w.ResponseWriter.Written() {
		return
	}
	if n := w.window.Get(); n > 0 {
		w.ResponseWriter.Header().Set(llm.ContextWindowHeader, strconv.Itoa(n))
	}
	if largest, retry, refused := w.window.Refusal(); refused {
		w.ResponseWriter.Header().Set(llm.LargestAdmissibleHeader, strconv.Itoa(largest))
		w.ResponseWriter.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
	}
}

func (w *windowWriter) WriteHeaderNow() {
	w.apply()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *windowWriter) Write(b []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(b)
}

func (w *windowWriter) WriteString(s string) (int, error) {
	w.apply()
	return w.ResponseWriter.WriteString(s)
}
