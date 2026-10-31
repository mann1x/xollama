package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gin-gonic/gin"
)

// RealtimeHandler serves GET /v1/realtime?model=…: OpenAI's Realtime
// transcription session, which the engine speaks over a websocket. The engine
// holds one session per process and refuses the rest with 503, and a model with
// no live mode with 501; both reach the client as the engine wrote them. The
// model stays loaded for as long as the connection does.
func (s *Server) RealtimeHandler(c *gin.Context) {
	name := mediaName(c.Query("model"), CapabilityTranscription)
	if _, ok := mediaModel(c, name); !ok {
		return
	}
	ctx := c.Request.Context()
	r, _, err := s.mediaRunner(ctx, name, CapabilityTranscription, nil)
	if err != nil {
		mediaRunnerError(c, err, name, CapabilityTranscription)
		return
	}
	release, err := mediaQueues.acquire(ctx, r.ModelPath()+"/stt")
	if err != nil {
		if errors.Is(err, errMediaBusy) {
			mediaError(c, http.StatusServiceUnavailable, fmt.Sprintf("the stt engine of %q is busy; try again later", name))
		}
		return
	}
	defer release()

	proxyRealtime(c, r.GetPort())
}

// proxyRealtime hands the connection to the engine. ReverseProxy carries the
// protocol switch itself, and returns when either side closes.
func proxyRealtime(c *gin.Context, port int) {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.URL.Path = "/v1/realtime"
			p.Out.Host = target.Host
			p.Out.Header["Upgrade"] = p.In.Header["Upgrade"]
			p.Out.Header["Connection"] = p.In.Header["Connection"]
		},
		ErrorHandler: func(_ http.ResponseWriter, _ *http.Request, err error) {
			mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}
