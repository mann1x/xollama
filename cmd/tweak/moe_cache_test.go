package tweak

import (
	"slices"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// --moe-cache takes auto, off or a size; the share and the warm start have
// nothing to act on under off; all three may be the server's default too.
func TestTheMoECacheFieldsStoreWhatTheEngineTakes(t *testing.T) {
	mode, ok := fieldByName("moe-cache")
	if !ok {
		t.Fatal("no moe-cache field")
	}
	c := &xollama.Config{Version: 1}
	for in, want := range map[string]string{"auto": "auto", "OFF": "off", "4096": "4096", "unset": ""} {
		if err := mode.set(c, in); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := mode.get(c); got != want {
			t.Fatalf("%q stored as %q, want %q", in, got, want)
		}
	}
	if err := mode.set(c, "big"); err == nil {
		t.Fatal("moe-cache big was accepted")
	}
	share, _ := fieldByName("moe-cache-cpu-share")
	if err := share.set(c, "0.3"); err != nil || share.get(c) == "" {
		t.Fatalf("share 0.3: %v %q", err, share.get(c))
	}
	if err := share.set(c, "2"); err == nil {
		t.Fatal("a share of 2 was accepted")
	}
	_ = mode.set(c, "off")
	if why := share.blocked(c); !strings.Contains(why, "moe-cache off") {
		t.Fatalf("share under off: blocked %q", why)
	}
	warm, _ := fieldByName("moe-cache-warm-start")
	if why := warm.blocked(c); why == "" {
		t.Fatal("the warm start is offered under moe-cache off")
	}
	for _, name := range []string{"moe-cache", "moe-cache-cpu-share", "moe-cache-warm-start"} {
		if !slices.Contains(serverFields(), name) {
			t.Fatalf("%s is not offered as a server default", name)
		}
	}
}

func TestTheMoECacheLineSaysWhatTheEngineDid(t *testing.T) {
	n := func(v int) *int { return &v }
	reason := "auto sizing is off"
	for _, tc := range []struct {
		d    api.MoECacheDevice
		want string
	}{
		{
			api.MoECacheDevice{Verdict: "pays", CacheMiB: n(2452), HostExpertMiB: n(13369), LayersRAMFit: n(34), LayersRAMFinal: n(41)},
			"on (automatic: measured to pay), 2452 MiB for 13369 MiB of experts in RAM, 41 expert layers in RAM (the fit alone: 34)",
		},
		{
			api.MoECacheDevice{Verdict: "explicit", CacheMiB: n(1999), Profile: &api.MoECacheProfile{Read: true, WarmExperts: n(1083)}},
			"on (fixed size), 1999 MiB, warm start 1083 experts",
		},
		{api.MoECacheDevice{Verdict: "off", Reason: &reason}, ""},
		{api.MoECacheDevice{Verdict: "multi_gpu"}, "off (automatic sizing is for one GPU"},
	} {
		if got := MoECacheText(tc.d); !strings.HasPrefix(got, tc.want) || (tc.want == "" && got != "") {
			t.Errorf("%s: %q, want %q", tc.d.Verdict, got, tc.want)
		}
	}
}
