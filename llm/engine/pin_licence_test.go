package engine

import "testing"

// licensedSidecars names, for a sidecar whose licence obliges its text to
// travel with the binary, the row that carries that text. oc-espeak is
// eSpeak-ng (GPL 3.0 or later); oc-codec is ffmpeg and lame (LGPL), whose
// source and build references are in the engine build's BUILD_INFO.md.
var licensedSidecars = map[string][]string{
	"espeak": {"espeak-licence", "build-info"},
	"codec":  {"build-info"},
}

// A package is staged from the pin's rows for its arch and from nothing else,
// so a library without its licence row ships without its licence.
func TestEveryLicensedSidecarShipsItsText(t *testing.T) {
	p, err := ParsePin(pinText)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"x86_64", "win-x86_64", "aarch64"} {
		have := map[string]bool{}
		for _, s := range p.Sidecars(arch) {
			have[s.Role] = true
		}
		for lib, texts := range licensedSidecars {
			if !have[lib] {
				continue
			}
			for _, text := range texts {
				if !have[text] {
					t.Errorf("%s: the pin stages the %s sidecar without a %q row beside it", arch, lib, text)
				}
			}
		}
	}
}
