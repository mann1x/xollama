package engine

import (
	"strings"
	"testing"
)

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want int
		ok   bool
	}{
		{"llamafile-v0.10.5+opencoti.c7", 7, true},
		{"llamafile-v0.10.5+opencoti.c8", 8, true},
		{"llamafile-v0.10.3+opencoti.c10", 10, true},
		// r2 is c7 plus one fix; it is still c7 for capability purposes.
		{"llamafile-v0.10.5+opencoti.c7-r2", 7, true},
		{"llamafile-v0.10.5", 0, false},
		{"", 0, false},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			got, ok := Cut(tc.tag)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Cut(%q) = %d, %v; want %d, %v", tc.tag, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestSlidingWindowRingByCut pins which cuts have the flags, against named
// tags rather than against whatever is pinned today. The flags are real -- they
// are in opencoti's development tree, added by patch 0288 -- but the c7 chain
// ends at 0244, and c7-r2 is c7 plus 0253 alone. Confirmed against the patch
// chain, not the documentation, which described the development tree without
// saying so.
func TestSlidingWindowRingByCut(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		{"llamafile-v0.10.5+opencoti.c7", false},
		{"llamafile-v0.10.5+opencoti.c7-r2", false},
		{"llamafile-v0.10.3+opencoti.c6", false},
		{"llamafile-v0.10.5+opencoti.c8", true},
		{"llamafile-v0.11.0+opencoti.c9", true},
		// A tag we cannot read is treated as not having it: refusing a load is
		// recoverable, sending a flag the engine rejects is not.
		{"llamafile-v0.10.5", false},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			if got := tagHasSlidingWindowRing(tc.tag); got != tc.want {
				t.Errorf("tagHasSlidingWindowRing(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}

// TestSlidingWindowRingIsRefusedOnTheShippedCut asserts the consequence for the
// artifact actually pinned, and that the refusal says something the operator
// can act on.
func TestSlidingWindowRingIsRefusedOnTheShippedCut(t *testing.T) {
	pin, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}

	why := SlidingWindowRingUnavailable()
	if tagHasSlidingWindowRing(pin.Tag) {
		if why != "" {
			t.Errorf("pinned %s has the ring, but it is refused: %s", pin.Tag, why)
		}
		return
	}
	if why == "" {
		t.Fatalf("pinned %s has no --cache-type-k-swa, but the ring was allowed through", pin.Tag)
	}
	// The message has to name the build and the way out, or it is just another
	// "invalid argument" with a different prefix.
	for _, want := range []string{pin.Tag, "--cache-type-k-swa", "kv.k"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal does not mention %q: %s", want, why)
		}
	}
}
