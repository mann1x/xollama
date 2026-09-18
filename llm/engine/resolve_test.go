package engine

import "testing"

func TestResolve(t *testing.T) {
	linux := Platform{OS: "linux", Arch: "amd64"}
	mac := Platform{OS: "darwin", Arch: "arm64"}

	cases := []struct {
		name     string
		p        Platform
		devices  []Device
		selector string
		want     Kind
	}{
		{"auto routes a tested backend to opencoti", linux, []Device{ada()}, "", KindOpencoti},
		{"auto treats an empty device list as CPU", linux, nil, "", KindOpencoti},
		{"auto keeps rocm on llama.cpp", linux, []Device{{Backend: BackendROCm}}, "", KindLlamaCpp},
		{"auto keeps an untested platform on llama.cpp", mac, []Device{{Backend: BackendMetal}}, "auto", KindLlamaCpp},

		// The case the policy exists for: one supported and one unsupported
		// device in the same load is not "probably fine".
		{"a mixed load falls back", linux, []Device{ada(), {Backend: BackendROCm}}, "", KindLlamaCpp},

		// linux/amd64 + CUDA is a tested row, so this reaches llama.cpp only
		// because the capability floor is consulted as well as the backend.
		{"a volta card falls back on a tested backend", linux, []Device{{BackendCUDA, 7, 0}}, "", KindLlamaCpp},
		{"a mixed old/new CUDA load falls back", linux, []Device{ada(), {BackendCUDA, 6, 1}}, "", KindLlamaCpp},

		{"llamacpp is honoured", linux, []Device{ada()}, "llamacpp", KindLlamaCpp},
		{"llamacpp is honoured case-insensitively", linux, []Device{ada()}, "  LlamaCpp ", KindLlamaCpp},
		{"opencoti is forced past the policy", mac, []Device{{Backend: BackendMetal}}, "opencoti", KindOpencoti},
		{"an unrecognised selector falls back to auto", linux, []Device{ada()}, "banana", KindOpencoti},
		{"an unrecognised selector still respects the policy", linux, []Device{{Backend: BackendROCm}}, "banana", KindLlamaCpp},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.p, tt.devices, tt.selector)
			if got.Kind != tt.want {
				t.Errorf("Resolve(%v, %v, %q) = %s (%s), want %s",
					tt.p, tt.devices, tt.selector, got.Kind, got.Reason, tt.want)
			}
			if got.Reason == "" {
				t.Error("Decision.Reason is empty; it is what gets logged")
			}
		})
	}
}

func TestResolveSaysWhenItIgnoredASelector(t *testing.T) {
	got := Resolve(Platform{"linux", "amd64"}, []Device{ada()}, "banana")
	if !contains(got.Reason, "unrecognised") {
		t.Errorf("Reason = %q, want it to mention the ignored selector", got.Reason)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
