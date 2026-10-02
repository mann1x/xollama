package server

import (
	"fmt"
	"image"
	_ "image/jpeg" // edit inputs
	_ "image/png"
	"math"
	"mime/multipart"

	_ "golang.org/x/image/webp"

	"github.com/ollama/ollama/types/xollama"
)

// editSize is the size an edit that names none comes back at: its first
// input image's, scaled down to the template's pixel count when larger (a
// phone photo must not cost more than the template's own image), in
// multiples of 16. Without it the engine answers at the template's size,
// and a 512x512 source comes back 1024x1024.
func editSize(form *multipart.Form, d *xollama.ImageDefaults) (string, bool) {
	var fh *multipart.FileHeader
	for _, k := range []string{"image[]", "image"} {
		if fs := form.File[k]; len(fs) > 0 {
			fh = fs[0]
			break
		}
	}
	if fh == nil {
		return "", false
	}
	f, err := fh.Open()
	if err != nil {
		return "", false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", false
	}
	budget := 1024.0 * 1024
	if d != nil && d.Width > 0 && d.Height > 0 {
		budget = float64(d.Width * d.Height)
	}
	w, h := float64(cfg.Width), float64(cfg.Height)
	if w*h > budget {
		s := math.Sqrt(budget / (w * h))
		w, h = w*s, h*s
	}
	return fmt.Sprintf("%dx%d", max(16, int(w)/16*16), max(16, int(h)/16*16)), true
}
