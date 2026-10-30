package engine

import (
	"os"
	"regexp"
	"testing"
)

func TestAGatedArchitectureIsServedOnlyOnceThePinDeclaresIt(t *testing.T) {
	withPin(t, Pin{Tag: "c13"})
	if ServesArch("lfm3") {
		t.Fatal("lfm3 is served by a pin that declares no arch_lfm3_v1")
	}

	withPin(t, Pin{Tag: "c14", Features: []string{"arch_lfm3_v1"}})
	if !ServesArch("lfm3") {
		t.Fatal("the pin declares arch_lfm3_v1, yet lfm3 is not served")
	}
}

func TestAnArchitectureTheTableDoesNotGateIsTheEngines(t *testing.T) {
	withPin(t, Pin{Tag: "c13"})
	for _, arch := range []string{"llama", "lfm2", "qwen35", ""} {
		if !ServesArch(arch) {
			t.Errorf("%q was taken from the engine", arch)
		}
	}
}

// scripts/check-engine-archs.sh reads the table as text, one
// `"<arch>": "<feature>",` per line. A row it cannot see would fail a sync
// that is in fact covered.
func TestTheSyncCheckCanReadEveryRow(t *testing.T) {
	src, err := os.ReadFile("arch.go")
	if err != nil {
		t.Fatal(err)
	}
	for arch, feature := range archFeatures {
		row := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(arch) + `":\s*"` + regexp.QuoteMeta(feature) + `",`)
		if !row.Match(src) {
			t.Errorf("row %q is not written as one line the sync check reads", arch)
		}
	}
}
