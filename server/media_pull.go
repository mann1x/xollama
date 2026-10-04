package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/internal/mediahub"
	"github.com/ollama/ollama/types/model"
)

var mediaDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// mediaResolve asks the hub for a file's digest; tests swap it.
var mediaResolve = func(ctx context.Context, r mediahub.Ref) (mediahub.File, error) {
	return mediahub.Resolve(ctx, &http.Client{Timeout: 30 * time.Second}, r)
}

// mediaDownload fetches a blob with the downloader every pull uses; tests
// swap it.
var mediaDownload = func(ctx context.Context, registry, digest string, fn func(api.ProgressResponse)) error {
	_, err := downloadBlob(ctx, downloadOpts{n: model.ParseName(registry), digest: digest, regOpts: &registryOptions{}, fn: fn})
	return err
}

// MediaPullHandler fetches one media component from Hugging Face into the
// store. Hugging Face's ollama-compatible registry serves a repo's large
// files by sha256, so this is the downloader an hf.co pull uses -- resumable,
// parallel, digest-verified -- pointed at one blob instead of a manifest.
//
// xollama-hook: media (registered in GenerateRoutes)
func (s *Server) MediaPullHandler(c *gin.Context) {
	var req api.MediaPullRequest
	switch err := c.ShouldBindJSON(&req); {
	case errors.Is(err, io.EOF):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing request body"})
		return
	case err != nil:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ref, err := mediahub.ParseRef(req.Source)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Digest != "" && !mediaDigestRE.MatchString(req.Digest) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%q is not a sha256 digest", req.Digest)})
		return
	}

	ch := make(chan any)
	go func() {
		defer close(ch)
		ctx, cancel := context.WithCancel(c.Request.Context())
		defer cancel()
		digest := req.Digest
		if digest == "" {
			ch <- api.ProgressResponse{Status: "resolving " + ref.String()}
			f, err := mediaResolve(ctx, ref)
			if err != nil {
				ch <- gin.H{"error": err.Error()}
				return
			}
			digest = f.Digest
		}
		fn := func(r api.ProgressResponse) { ch <- r }
		if err := mediaDownload(ctx, ref.Registry(), digest, fn); err != nil {
			ch <- gin.H{"error": err.Error()}
			return
		}
		ch <- api.ProgressResponse{Status: "success", Digest: digest}
	}()

	if req.Stream != nil && !*req.Stream {
		waitForStream(c, ch)
		return
	}
	streamResponse(c, ch)
}
