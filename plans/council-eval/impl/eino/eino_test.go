package eino

import (
	"testing"

	"councileval/council"
	"councileval/suite"
)

func newRunner() council.Runner { return &Runner{} }

func TestCouncil(t *testing.T)      { suite.Tests(t, newRunner) }
func BenchmarkCouncil(b *testing.B) { suite.Bench(b, newRunner) }
