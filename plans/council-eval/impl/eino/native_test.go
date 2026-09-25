package eino

import (
	"testing"

	"councileval/council"
	"councileval/suite"
)

// The native-stream variant runs the same suite; it is an experiment, the
// closure Runner in eino_test.go is the candidate.
func newNative() council.Runner { return &Runner{Native: true} }

func TestNativeCouncil(t *testing.T)      { suite.Tests(t, newNative) }
func BenchmarkNativeCouncil(b *testing.B) { suite.Bench(b, newNative) }
