package llm

import (
	"errors"
	"slices"
	"testing"
)

func TestValidIntrospectEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to props", "", "props"},
		{"whitespace defaults to props", "   ", "props"},
		{"a leading slash is tolerated", "/props", "props"},
		{"a trailing slash is tolerated", "slots/", "slots"},
		{"an opencoti path with a slash inside it", "polykv/pools", "polykv/pools"},
		{"metrics", "metrics", "metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidIntrospectEndpoint(tc.in)
			if err != nil {
				t.Fatalf("ValidIntrospectEndpoint(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidIntrospectEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidIntrospectEndpointRefusesAnythingElse is the whole reason the path
// is a name from a list rather than something the caller composes.
//
// llama-server trusts whoever can reach it: its surface includes POST
// /completion, /apply-template, and on some builds slot save and restore to
// file paths of the caller's choosing. A proxy that forwarded an arbitrary path
// would hand all of that to anyone who can reach the ollama port, which is a
// much larger set of people.
func TestValidIntrospectEndpointRefusesAnythingElse(t *testing.T) {
	for _, endpoint := range []string{
		"completion",
		"apply-template",
		"embedding",
		"slots/0?action=restore",
		"../../../etc/passwd",
		"..%2f..%2fprops",
		"props/../completion",
		"health\r\nX-Injected: 1",
		"http://elsewhere.example/props",
		"//elsewhere.example/props",
	} {
		t.Run(endpoint, func(t *testing.T) {
			got, err := ValidIntrospectEndpoint(endpoint)
			if err == nil {
				t.Fatalf("ValidIntrospectEndpoint(%q) = %q, want a refusal", endpoint, got)
			}
			if !errors.Is(err, ErrUnknownIntrospectEndpoint) {
				t.Errorf("error = %v, want it to wrap ErrUnknownIntrospectEndpoint so a caller can tell it from a transport failure", err)
			}
		})
	}
}

// TestIntrospectEndpointsIsACopy guards the whitelist against a caller that
// writes through the returned slice.
//
// Appending is not the hazard and would not catch this: the package-level slice
// is a literal, so len == cap and append always reallocates. Assigning into it
// is the one that reaches the original, and it is what turns a listing call
// into a way to widen the whitelist.
func TestIntrospectEndpointsIsACopy(t *testing.T) {
	before := slices.Clone(introspectEndpoints)

	got := IntrospectEndpoints()
	got[0] = "completion"

	if !slices.Equal(introspectEndpoints, before) {
		t.Fatalf("the whitelist became %v, want it unchanged at %v", introspectEndpoints, before)
	}
	if _, err := ValidIntrospectEndpoint("completion"); err == nil {
		t.Error("a caller writing through the returned slice widened the whitelist")
	}
}

// TestIntrospectEndpointsCoverBothEngines documents why stock llama.cpp is not
// excluded: three of the four endpoints are upstream's own, so the window is
// useful there too, and the one that is not answers 404 — which is itself the
// honest report that the feature is absent.
func TestIntrospectEndpointsCoverBothEngines(t *testing.T) {
	for _, upstream := range []string{"props", "slots", "metrics"} {
		if !slices.Contains(introspectEndpoints, upstream) {
			t.Errorf("%q is an upstream endpoint and should be readable on either engine", upstream)
		}
	}
	if !slices.Contains(introspectEndpoints, "polykv/pools") {
		t.Error("the opencoti pool listing should be readable")
	}
}

func TestEngineName(t *testing.T) {
	if got := (&llamaServerRunner{usedOpencoti: true}).EngineName(); got != "opencoti" {
		t.Errorf("EngineName() = %q, want opencoti", got)
	}
	if got := (&llamaServerRunner{}).EngineName(); got != "llamacpp" {
		t.Errorf("EngineName() = %q, want llamacpp", got)
	}
}

// TestEngineGetBeforeTheEngineListens covers the window between a runner
// existing and its process being up.
func TestEngineGetBeforeTheEngineListens(t *testing.T) {
	if _, _, err := (&llamaServerRunner{}).EngineGet(t.Context(), "props"); err == nil {
		t.Fatal("EngineGet() on a runner with no port = nil error, want a refusal")
	}
}

// TestEngineGetValidatesBeforeDialing proves the refusal happens before any
// network work, so a bad path cannot reach the engine even in principle.
func TestEngineGetValidatesBeforeDialing(t *testing.T) {
	// Port 1 would fail to connect; the whitelist must reject first.
	_, _, err := (&llamaServerRunner{port: 1}).EngineGet(t.Context(), "completion")
	if !errors.Is(err, ErrUnknownIntrospectEndpoint) {
		t.Fatalf("error = %v, want the whitelist refusal rather than a dial failure", err)
	}
}
