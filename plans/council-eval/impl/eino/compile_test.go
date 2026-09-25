package eino

import "testing"

// BenchmarkCompile is what a request would pay if the graph were built and
// compiled per request instead of once per Runner.
func BenchmarkCompile(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := build(DefaultSlots, false); err != nil {
			b.Fatal(err)
		}
	}
}
