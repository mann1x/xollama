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
// devices are the accelerators this load will actually use. An empty list
// means the load is CPU-only, which is a tested configuration, not an unknown
// one.
//
// Under "auto" every device in play must be supported, not just one of them: a
// load spanning a supported and an unsupported device is exactly the case
// where "probably fine" is wrong.
func Resolve(p Platform, devices []Device, selector string) Decision {
	switch s := strings.ToLower(strings.TrimSpace(selector)); s {
	case string(KindLlamaCpp):
		return Decision{KindLlamaCpp, EnvSelector + "=llamacpp"}
	case string(KindOpencoti):
		return Decision{KindOpencoti, EnvSelector + "=opencoti (forced, policy not consulted)"}
	case "", "auto":
		return auto(p, devices, "")
	default:
		return auto(p, devices, fmt.Sprintf("ignored unrecognised %s=%q; ", EnvSelector, selector))
	}
}

func auto(p Platform, devices []Device, prefix string) Decision {
	if len(devices) == 0 {
		devices = []Device{{Backend: BackendCPU}}
	}
	for _, d := range devices {
		if why := deviceUnsupported(p, d); why != "" {
			return Decision{KindLlamaCpp, prefix + why}
		}
	}
	return Decision{KindOpencoti, fmt.Sprintf(
		"%sopencoti-llamafile is tested on %s/%s with %s", prefix, p.OS, p.Arch, join(devices))}
}

func join(devices []Device) string {
	s := make([]string, len(devices))
	for i, d := range devices {
		s[i] = d.String()
	}
	return strings.Join(s, ",")
}
