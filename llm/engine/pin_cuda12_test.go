package engine

import (
	"strings"
	"testing"
)

// The CUDA 12 payload rides on "#!" lines (opencoti #498), so every shipped
// pin parser keeps reading the pin and ours reads the payload.
const cuda12PinText = `repo ManniX-ITA/opencoti-llamafile-dev
rev 7b836910ac880e898aa08bd43665d1f6a3b7d31c
tag opencoti-0.10.5-c7-2609271900001
channel dev
bin x86_64 builds/x/opencoti-x 30df5425a067cc66cdbb525157fbc8fc6aa75fae8cbf32bf26a19b18d7397412
dso x86_64 builds/x/ggml-cuda.so a27685f2a3c45445dbf4399975eb458fbefcd3f71cbe05ed66de2cc9c30055c7
#! cuda-sass 86 120
#! dso-cuda12 x86_64 builds/x/ggml-cuda-cu12-x86_64.so 6666afc2a99f7febdfc661bd3dbb01c3589d14d12285320651818487075aade6
#! cuda12-sass 70
`

func TestTheCUDA12PayloadIsReadFromItsDirectives(t *testing.T) {
	p, err := ParsePin(cuda12PinText)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := p.CUDA12DSO("x86_64"); !ok || !strings.HasSuffix(a.Path, "ggml-cuda-cu12-x86_64.so") {
		t.Fatalf("CUDA12DSO = %+v, %v", a, ok)
	}
	if d, _ := p.DSO("x86_64"); !strings.HasSuffix(d.Path, "/ggml-cuda.so") {
		t.Fatalf("the CUDA 13 dso row was replaced: %+v", d)
	}
	if !p.CoversCUDA12(7, 0) || p.CoversCUDA12(8, 6) || p.CoversCUDA12(6, 1) {
		t.Fatalf("cuda12-sass not in force: %v", p.CUDA12SASS)
	}
	// Without the directive the CUDA 12 payload covers nothing.
	plain, err := ParsePin(strings.Split(cuda12PinText, "#! dso-cuda12")[0])
	if err != nil {
		t.Fatal(err)
	}
	if plain.CoversCUDA12(7, 0) {
		t.Fatal("a pin with no CUDA 12 payload claims to cover 7.0")
	}
}

// A V100 is the CUDA 12 payload's; an RTX 3090 is the CUDA 13 one's; one load
// across both cannot be served by one engine process and goes to llama.cpp.
func TestEachLoadRunsOnTheCUDAPayloadItsGPUsNeed(t *testing.T) {
	p, err := ParsePin(cuda12PinText)
	if err != nil {
		t.Fatal(err)
	}
	restore := loadPin
	loadPin = func() (Pin, error) { return p, nil }
	t.Cleanup(func() { loadPin = restore })

	v100 := Device{Backend: BackendCUDA, ComputeMajor: 7, ComputeMinor: 0}
	rtx3090 := Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 6}
	a100 := Device{Backend: BackendCUDA, ComputeMajor: 8, ComputeMinor: 0}
	linux := Platform{OS: "linux", Arch: "amd64"}

	if why := deviceUnsupported(linux, v100); why != "" {
		t.Errorf("V100 refused: %s", why)
	}
	if why := deviceUnsupported(linux, a100); why == "" {
		t.Error("A100 (8.0) is routed to payloads with no code for it")
	}
	for _, tt := range []struct {
		name    string
		devices []Device
		cuda12  bool
		refused bool
	}{
		{"V100", []Device{v100}, true, false},
		{"RTX 3090", []Device{rtx3090}, false, false},
		{"V100 + RTX 3090", []Device{v100, rtx3090}, false, true},
	} {
		cuda12, why := cudaPayload(p, tt.devices)
		if cuda12 != tt.cuda12 || (why != "") != tt.refused {
			t.Errorf("%s: cudaPayload = %v, %q", tt.name, cuda12, why)
		}
	}
}
