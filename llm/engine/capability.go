package engine

import (
	"fmt"
	"regexp"
	"strconv"
)

// Engine capabilities that the published artifacts do not all have.
//
// The pin file names the exact bytes this build ships, and those bytes are the
// only authority on what argv they accept. A flag that exists in opencoti's
// development tree is not a flag this build can send: the published cut is
// older, and it rejects the whole command line rather than ignoring what it
// does not know.
//
// So a feature whose flag is not in the shipped cut is refused where the
// operator set it, naming the engine and the build, rather than being sent and
// turned into "error: invalid argument" from a subprocess -- which names
// nothing the operator can act on.

// ringCut is the first opencoti cut whose arg.cpp carries
// --cache-type-k-swa / --cache-type-v-swa.
//
// They are added by patch 0288 (kvarn-serve). The c7 patch chain ends at 0244,
// and c7-r2 is by construction c7 plus 0253 (the rolling-KV window abort fix)
// and nothing else -- no flags, no features. So c8 is the first cut that has
// them, confirmed against the patch chain rather than the documentation, which
// described the development tree without saying so.
const ringCut = 8

// cutPattern pulls the cut number out of a release tag such as
// "llamafile-v0.10.5+opencoti.c7". A tag that does not carry one is treated as
// not having the capability: refusing a load is recoverable, and sending a flag
// the engine rejects is not.
var cutPattern = regexp.MustCompile(`\.c(\d+)`)

// Cut returns the opencoti cut number in a release tag, and whether it had one.
func Cut(tag string) (int, bool) {
	m := cutPattern.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// HasSlidingWindowRing reports whether the engine this build ships can be told
// to quantise the sliding-window half of the cache separately.
func HasSlidingWindowRing() bool {
	pin, err := DefaultPin()
	if err != nil {
		return false
	}
	return tagHasSlidingWindowRing(pin.Tag)
}

// tagHasSlidingWindowRing is the decision, separated from where the tag comes
// from so it can be asserted against known tags rather than against whatever
// happens to be pinned today.
func tagHasSlidingWindowRing(tag string) bool {
	cut, ok := Cut(tag)
	return ok && cut >= ringCut
}

// SlidingWindowRingUnavailable explains, in the operator's terms, why the ring
// cannot be set on this build. Empty when it can.
func SlidingWindowRingUnavailable() string {
	if HasSlidingWindowRing() {
		return ""
	}
	tag := "unknown"
	if pin, err := DefaultPin(); err == nil && pin.Tag != "" {
		tag = pin.Tag
	}
	return fmt.Sprintf("the engine this build ships (%s) has no --cache-type-k-swa/--cache-type-v-swa; "+
		"they arrive in opencoti c%d. Set only kv.k and kv.v, which quantise both halves together on this engine",
		tag, ringCut)
}
