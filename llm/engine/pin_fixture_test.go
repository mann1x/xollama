package engine

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// A synthetic pin directory for the tests: component pins as text, and the
// index that names them written from their bytes.
const (
	fixEngine = "2610040837001"
	fixLib    = "2610040656001"
	fixRev    = "21462d105075e8d0a3f511d5519077761a1e2efb"
	fixPinRev = "03702be6e5513862d493e5ebda82ed85e9c4f250"
)

var (
	fixGGML  = strings.Repeat("1", 64)
	fixMedia = strings.Repeat("2", 64)
)

// sum is the sha256 of a body, as a pin states it.
func sum(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }

// row is a `file` row for a file whose bytes are body.
func row(platform, kind, path, body string, more ...string) string {
	return fmt.Sprintf("file %s %s %s %s %d %s", platform, kind, path, sum(body), len(body), strings.Join(more, " "))
}

// enginePin is an engine component pin with these extra statements.
func enginePin(rows ...string) string {
	return "format 2\ncomponent engine\nversion " + fixEngine + "\nrepo o/r\nrev " + fixRev +
		"\nabi ggml " + fixGGML + "\nabi media " + fixMedia + "\nabi-source computed\n" + strings.Join(rows, "\n") + "\n"
}

// libPin is any other component's pin, built for and from the fixture engine.
func libPin(name string, rows ...string) string {
	abi := "ggml " + fixGGML
	if name == "media" {
		abi = "media " + fixMedia
	}
	return "format 2\ncomponent " + name + "\nversion " + fixLib + "\nbuilt-from " + fixEngine +
		"\nrepo o/r\nrev " + fixRev + "\nabi " + abi + "\nabi-source computed\nengine-min " + fixEngine +
		"\n" + strings.Join(rows, "\n") + "\n"
}

var (
	componentRe = regexp.MustCompile(`(?m)^component\s+(\S+)`)
	versionRe   = regexp.MustCompile(`(?m)^version\s+(\S+)`)
)

// pinFiles is the pin directory holding these component pins: each as
// <component>.txt, and the index naming it by the sha256 of its bytes.
func pinFiles(comps ...string) map[string]string {
	files := map[string]string{}
	index := "format 2\nchannel dev\ntag c8-dev\n"
	for _, text := range comps {
		name := componentRe.FindStringSubmatch(text)[1]
		files[name+".txt"] = text
		index += fmt.Sprintf("%s pin/%s.txt rev %s sha256 %s version %s\n",
			name, name, fixPinRev, sum(text), versionRe.FindStringSubmatch(text)[1])
	}
	files["index.txt"] = index
	return files
}

// loadFiles reads a pin directory given as file name -> text.
func loadFiles(files map[string]string) (Pin, error) {
	fsys := fstest.MapFS{}
	for name, text := range files {
		fsys["pin/"+name] = &fstest.MapFile{Data: []byte(text)}
	}
	return LoadPin(fsys, "pin")
}

// mustLoad is loadFiles(pinFiles(comps...)) for a pin that has to parse.
func mustLoad(t *testing.T, comps ...string) Pin {
	t.Helper()
	p, err := loadFiles(pinFiles(comps...))
	if err != nil {
		t.Fatalf("LoadPin: %v", err)
	}
	return p
}
