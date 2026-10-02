package server

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/types/xollama"
)

func pngOf(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestAnEditWithoutASizeKeepsItsSourcesWithinTheTemplatesPixels(t *testing.T) {
	mediaStore(t)
	s, e := mediaServer(t, map[string]*xollama.Media{"klein": kleinMedia(t, "reference")})
	for _, tt := range []struct {
		name   string
		fields map[string]string
		w, h   int
		want   string
	}{
		{"a small source keeps its size", nil, 512, 512, "512x512"},
		{"an odd size rounds down to 16", nil, 500, 333, "496x320"},
		{"a large source is scaled to the template's pixels", nil, 2048, 1536, "1168x880"},
		{"auto is the source's size", map[string]string{"size": "auto"}, 640, 480, "640x480"},
		{"the client's size wins", map[string]string{"size": "768x768"}, 512, 512, "768x768"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]string{"model": "klein", "prompt": "make it blue"}
			for k, v := range tt.fields {
				fields[k] = v
			}
			body, ct := multipartBody(t, fields, map[string][]string{"image[]": {pngOf(t, tt.w, tt.h)}})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", body)
			c.Request.Header.Set("Content-Type", ct)
			s.ImageEditsHandler(c)
			if w.Code != http.StatusOK {
				t.Fatalf("edit = %d %s", w.Code, w.Body.String())
			}
			if got := engineForm(t, e.last(t)).Value["size"]; len(got) != 1 || got[0] != tt.want {
				t.Errorf("size = %v, want %s", got, tt.want)
			}
		})
	}
}
