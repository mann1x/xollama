package engine

import (
	"fmt"
	"strings"
)

// EnvSelector names the variable that overrides the routing policy:
// "auto" (the default), "opencoti", or "llamacpp".
const EnvSelector = "XOLLAMA_ENGINE"

// Decision is the chosen engine and why, so the reason can be logged once
// rather than re-derived from the policy table by whoever reads the log.
type Decision struct {
	Kind   Kind
	Reason string
}

// Resolve picks the engine for one load.
//
// backends are the Library names of the devices this load will actually use.
// An empty list means the load is CPU-only, which is a tested configuration,
// not an unknown one.
//
// Under "auto" every backend in play must be tested, not just one of them: a
// load spanning a supported and an unsupported device is exactly the case
// where "probably fine" is wrong.
func Resolve(p Platform, backends []Backend, selector string) Decision {
	switch s := strings.ToLower(strings.TrimSpace(selector)); s {
	case string(KindLlamaCpp):
		return Decision{KindLlamaCpp, EnvSelector + "=llamacpp"}
	case string(KindOpencoti):
		return Decision{KindOpencoti, EnvSelector + "=opencoti (forced, policy not consulted)"}
	case "", "auto":
		return auto(p, backends, "")
	default:
		return auto(p, backends, fmt.Sprintf("ignored unrecognised %s=%q; ", EnvSelector, selector))
	}
}

func auto(p Platform, backends []Backend, prefix string) Decision {
	if len(backends) == 0 {
		backends = []Backend{BackendCPU}
	}
	for _, b := range backends {
		if !Supports(p, b) {
			return Decision{KindLlamaCpp, fmt.Sprintf(
				"%sopencoti-llamafile is not tested on %s/%s with %s", prefix, p.OS, p.Arch, b)}
		}
	}
	return Decision{KindOpencoti, fmt.Sprintf(
		"%sopencoti-llamafile is tested on %s/%s with %s", prefix, p.OS, p.Arch, join(backends))}
}

func join(backends []Backend) string {
	s := make([]string, len(backends))
	for i, b := range backends {
		s[i] = string(b)
	}
	return strings.Join(s, ",")
}
