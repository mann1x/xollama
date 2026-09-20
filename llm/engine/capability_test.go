package engine

import (
	"strings"
	"testing"
)

// TestSlidingWindowRingFollowsTheDeclaration is the point of the file: the
// capability comes from what the pin SAYS the artifact carries, not from the
// shape of its tag. Inferring it from a cut number was wrong for exactly the
// case this fork now lives in -- a dev artifact carrying part of the next cut
// while still tagged as the current one.
func TestSlidingWindowRingFollowsTheDeclaration(t *testing.T) {
	const assets = "\nbin x86_64 a.llamafile " + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{
			name: "a release without it",
			text: "repo o/r\nrev 619e163221eebf248c49db7d16533130328e26f6\ntag v0.10.5+opencoti.c7\nchannel release" + assets,
			want: false,
		},
		{
			name: "a release with it",
			text: "repo o/r\nrev 619e163221eebf248c49db7d16533130328e26f6\ntag v0.10.5+opencoti.c8\nchannel release\nfeature swa-cache-types" + assets,
			want: true,
		},
		{
			// The case the tag regex got wrong: a dev build carrying patch 0288
			// while still tagged c7. Inferring from the tag refused the very
			// flags the build was pinned to test.
			name: "a dev build tagged as the older cut but carrying the flags",
			text: "repo o/r-dev\nrev 619e163221eebf248c49db7d16533130328e26f6\ntag v0.10.5+opencoti.c7-dev\nchannel dev\nfeature swa-cache-types" + assets,
			want: true,
		},
		{
			// And the reverse: a dev build on the c8 line that has not picked
			// up 0288 yet. A cut number would have promised it.
			name: "a dev build on the newer line that does not carry them yet",
			text: "repo o/r-dev\nrev 619e163221eebf248c49db7d16533130328e26f6\ntag v0.10.6+opencoti.c8-dev\nchannel dev" + assets,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePin(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.HasFeature(featureSWACacheTypes); got != tc.want {
				t.Errorf("HasFeature(%q) = %v, want %v", featureSWACacheTypes, got, tc.want)
			}
		})
	}
}

// TestSlidingWindowRingMatchesTheShippedPin asserts the consequence for the
// artifact actually pinned, and that a refusal says something actionable.
func TestSlidingWindowRingMatchesTheShippedPin(t *testing.T) {
	pin, err := DefaultPin()
	if err != nil {
		t.Fatal(err)
	}

	why := SlidingWindowRingUnavailable()
	if pin.HasFeature(featureSWACacheTypes) {
		if why != "" {
			t.Errorf("pinned %s declares the ring, but it is refused: %s", pin.Tag, why)
		}
		return
	}
	if why == "" {
		t.Fatalf("pinned %s does not declare the ring, but it was allowed through", pin.Tag)
	}
	// The message has to name the build and the way out, or it is just another
	// "invalid argument" with a different prefix.
	for _, want := range []string{pin.Tag, pin.Channel, "--cache-type-k-swa", "kv.k"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal does not mention %q: %s", want, why)
		}
	}
}
