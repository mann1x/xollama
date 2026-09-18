package engine

import "testing"

func TestResolve(t *testing.T) {
	linux := Platform{OS: "linux", Arch: "amd64"}
	mac := Platform{OS: "darwin", Arch: "arm64"}

	cases := []struct {
		name     string
		p        Platform
		backends []Backend
		selector string
		want     Kind
	}{
		{"auto routes a tested backend to opencoti", linux, []Backend{BackendCUDA}, "", KindOpencoti},
		{"auto treats an empty device list as CPU", linux, nil, "", KindOpencoti},
		{"auto keeps rocm on llama.cpp", linux, []Backend{BackendROCm}, "", KindLlamaCpp},
		{"auto keeps an untested platform on llama.cpp", mac, []Backend{BackendMetal}, "auto", KindLlamaCpp},

		// The case the policy exists for: one supported and one unsupported
		// device in the same load is not "probably fine".
		{"a mixed load falls back", linux, []Backend{BackendCUDA, BackendROCm}, "", KindLlamaCpp},

		{"llamacpp is honoured", linux, []Backend{BackendCUDA}, "llamacpp", KindLlamaCpp},
		{"llamacpp is honoured case-insensitively", linux, []Backend{BackendCUDA}, "  LlamaCpp ", KindLlamaCpp},
		{"opencoti is forced past the policy", mac, []Backend{BackendMetal}, "opencoti", KindOpencoti},
		{"an unrecognised selector falls back to auto", linux, []Backend{BackendCUDA}, "banana", KindOpencoti},
		{"an unrecognised selector still respects the policy", linux, []Backend{BackendROCm}, "banana", KindLlamaCpp},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.p, tt.backends, tt.selector)
			if got.Kind != tt.want {
				t.Errorf("Resolve(%v, %v, %q) = %s (%s), want %s",
					tt.p, tt.backends, tt.selector, got.Kind, got.Reason, tt.want)
			}
			if got.Reason == "" {
				t.Error("Decision.Reason is empty; it is what gets logged")
			}
		})
	}
}

func TestResolveSaysWhenItIgnoredASelector(t *testing.T) {
	got := Resolve(Platform{"linux", "amd64"}, []Backend{BackendCUDA}, "banana")
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
