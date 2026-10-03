package engine

import (
	"strings"
	"testing"
)

const sidecarPinHead = "repo o/r\nrev 619e163221eebf248c49db7d16533130328e26f6\ntag v1\nchannel dev\n"

func TestSidecarRowsAreReadFromTheirCommentLines(t *testing.T) {
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	p, err := ParsePin(sidecarPinHead +
		"bin x86_64 builds/v1/engine " + a + "\n" +
		"bin win-x86_64-gpu builds/v1/engine.exe " + a + "\n" +
		"#! sidecar  x86_64      codec     builds/v1/oc-codec-linux-x86_64.so    " + b + "\n" +
		"#! sidecar  x86_64      audiocpp  builds/v1/oc-audiocpp-linux-x86_64.so " + c + "\n" +
		"#! sidecar  win-x86_64  codec     builds/v1/oc-codec-win-x86_64.dll     " + b + "\n" +
		// A kind this build has never heard of is still a file to stage.
		"#! sidecar  aarch64     espeak-data builds/v1/oc-espeak-data-linux-aarch64.bin " + c + "\n" +
		"#! notakey  x86_64 whatever\n")
	if err != nil {
		t.Fatal(err)
	}
	got := p.Sidecars("x86_64")
	if len(got) != 2 || got[0].Role != SidecarCodec || got[1].Role != SidecarAudioCpp ||
		got[0].Path != "builds/v1/oc-codec-linux-x86_64.so" || got[1].SHA256 != c {
		t.Fatalf("x86_64 sidecars = %+v", got)
	}
	// The -gpu bin is the same platform: a sidecar has no GPU variant.
	if got := p.Sidecars("win-x86_64-gpu"); len(got) != 1 || got[0].Role != SidecarCodec {
		t.Fatalf("win-x86_64-gpu sidecars = %+v, want the win-x86_64 codec", got)
	}
	if got := p.Sidecars("aarch64"); len(got) != 1 || got[0].Role != "espeak-data" {
		t.Fatalf("aarch64 sidecars = %+v, want the one of a new kind", got)
	}
	if got := p.Sidecars("universal"); len(got) != 0 {
		t.Fatalf("universal sidecars = %+v, want none", got)
	}
	// A sidecar is neither the engine nor a GPU payload.
	if _, ok := p.DSO("x86_64"); ok {
		t.Fatal("a sidecar row was read as a dso")
	}
	if p.Accelerates("x86_64", BackendCUDA) {
		t.Fatal("a sidecar row made the pin claim CUDA")
	}
	if u := p.URL(got[0]); !strings.HasSuffix(u, "/builds/v1/oc-codec-linux-x86_64.so") {
		t.Fatalf("URL = %s", u)
	}
}

func TestAMalformedSidecarRowIsRefused(t *testing.T) {
	a := strings.Repeat("a", 64)
	bin := "bin x86_64 engine " + a + "\n"
	cases := map[string]string{
		"misshapen kind":         "#! sidecar x86_64 Codec builds/v1/x.so " + a,
		"kind with a path in it": "#! sidecar x86_64 a/b builds/v1/x.so " + a,
		"short row":              "#! sidecar x86_64 codec builds/v1/x.so",
		"long row":               "#! sidecar x86_64 codec builds/v1/x.so " + a + " extra",
		"bad sha":                "#! sidecar x86_64 codec builds/v1/x.so " + strings.Repeat("A", 64),
		"short sha":              "#! sidecar x86_64 codec builds/v1/x.so abc",
		"escaping path":          "#! sidecar x86_64 codec ../x.so " + a,
		"absolute path":          "#! sidecar x86_64 codec /x.so " + a,
		"same kind twice":        "#! sidecar x86_64 codec builds/v1/x.so " + a + "\n#! sidecar x86_64 codec builds/v1/y.so " + a,
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePin(sidecarPinHead + bin + row + "\n"); err == nil {
				t.Fatal("ParsePin accepted it; the engine would ship without the file or with the wrong one")
			}
		})
	}
}
