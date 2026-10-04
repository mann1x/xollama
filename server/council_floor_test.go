package server

import "testing"

// A model whose context is no larger than what its council asks for is still
// booked: the unstated floor does not insist on the whole window, which the
// root pool's cells make unreachable.
func TestAnUnstatedFloorDoesNotAskForTheWholeWindow(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		window, floor, reserve int
		stated, rootUnowned    bool
		want                   int
	}{
		// omni-council-idle: num_ctx 16384, six seats of 12288 and the templates.
		{"a 16k model, first turn", 16384, 16384, 74752, false, true, 8192},
		{"a 16k model, a later call on the owner", 16384, 16384, 74752, false, false, 8192},
		// omni-council-kv3-384k: num_ctx 196608 in a pool twice that.
		{"the benchmark tag", 196608, 196608, 99328, false, true, 98304},
		{"a small reserve is the floor", 131072, 131072, 20000, false, true, 20224},
		{"never under 4096", 8192, 8192, 100, false, false, 4096},
		// A stated floor is the operator's: all of it, except beside a root
		// that sits outside the window.
		{"a stated floor is kept", 16384, 16384, 74752, true, false, 16384},
		{"a stated floor beside an unowned root comes down to the reserve", 131072, 65536, 20000, true, true, 20224},
	} {
		tr := &councilTree{window: tc.window, floor: tc.floor, floorStated: tc.stated, reserve: tc.reserve}
		if got := tr.firstFloor(tc.rootUnowned); got != tc.want {
			t.Errorf("%s: firstFloor = %d, want %d", tc.name, got, tc.want)
		}
	}

	tr := &councilTree{window: 16384, floor: 16384, reserve: 74752}
	if p := tr.ownerWindow(); p.NumCtx != 16384 || p.NumCtxMin != 8192 {
		t.Errorf("before the grant: %+v, want [8192, 16384]", p)
	}
	tr.grant = 16128
	if p := tr.ownerWindow(); p.NumCtx != 16128 || p.NumCtxMin != 16128 {
		t.Errorf("after the grant: %+v, want the grant stated", p)
	}
}

func TestACouncilLargerThanItsContextIsRecognised(t *testing.T) {
	// omni-council-idle: six seats of 12288 and the templates in 16384.
	if !councilOutgrowsContext(74752, 16384) {
		t.Error("a 16k model with a 74k council is not seen as too small")
	}
	// The benchmark tag, and an engine that reported no window.
	if councilOutgrowsContext(99328, 196608) || councilOutgrowsContext(99328, 0) {
		t.Error("a council that fits, or an unknown window, is called too small")
	}
}
