package engine

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countDigests swaps digestOf for one that counts its calls.
func countDigests(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := digestOf
	digestOf = func(artifact string, o payloadOwner) string {
		n.Add(1)
		return orig(artifact, o)
	}
	t.Cleanup(func() { digestOf = orig })
	return &n
}

// TestTheSameBytesAtTwoPathsShareOnePayload: identity is content, not path.
// Two copies of one engine -- a re-install beside the old one, a staging copy
// -- keep the payload, and once each copy is known by its stat, alternating
// between them hashes nothing more.
func TestTheSameBytesAtTwoPathsShareOnePayload(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	a := artifactFile(t, dir, "a.llamafile", "one engine's bytes")
	b := artifactFile(t, filepath.Join(dir), "b.llamafile", "one engine's bytes")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(b, later, later); err != nil {
		t.Fatal(err)
	}

	PreparePayloadHome(a, root)
	kept := payloadTree(t, root, "its kernels")
	digests := countDigests(t)

	PreparePayloadHome(b, root)
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the same bytes at another path purged the payload: %v", err)
	}
	if got := digests.Load(); got != 1 {
		t.Fatalf("first launch from the second path hashed %d times, want 1", got)
	}
	for range 3 {
		PreparePayloadHome(a, root)
		PreparePayloadHome(b, root)
	}
	if got := digests.Load(); got != 1 {
		t.Fatalf("alternating between two known copies hashed %d times, want none after the first", got)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("alternating copies purged the payload: %v", err)
	}
}

// TestConcurrentLaunchesNeverPurgeEachOther: an LLM engine and a media engine
// start together. Each prepares the root, then its engine unpacks into it. No
// launch may purge a tree another launch has already been handed.
//
// The digest is held open until the launches have all arrived (or a timeout,
// which is what a serialized launch sees), so without the lock they all read
// "no marker" together; they then leave 30 ms apart, and each that leaves
// unpacks at once -- before the next one purges what it finds.
func TestConcurrentLaunchesNeverPurgeEachOther(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	art := artifactFile(t, dir, "engine.llamafile", "one engine")

	const n = 4
	var arrived atomic.Int32
	all := make(chan struct{})
	var once sync.Once
	orig := digestOf
	digestOf = func(artifact string, o payloadOwner) string {
		k := arrived.Add(1)
		if k == n {
			once.Do(func() { close(all) })
		}
		select {
		case <-all:
		case <-time.After(100 * time.Millisecond):
		}
		// Leave in arrival order, apart, so each launch that leaves has
		// unpacked before the next one decides what to purge.
		time.Sleep(time.Duration(k-1) * 30 * time.Millisecond)
		return orig(artifact, o)
	}
	t.Cleanup(func() { digestOf = orig })

	var wg sync.WaitGroup
	unpacked := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 20 * time.Millisecond)
			if got := PreparePayloadHome(art, root); got != root {
				t.Errorf("launch %d: PreparePayloadHome() = %q", i, got)
				return
			}
			d := filepath.Join(root, payloadDirName, "v", "launch"+string(rune('a'+i)))
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Error(err)
				return
			}
			unpacked[i] = filepath.Join(d, "ggml-cuda.so")
			if err := os.WriteFile(unpacked[i], []byte("kernels"), 0o644); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i, f := range unpacked {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("launch %d's unpacked payload was purged by a concurrent launch of the same engine: %v", i, err)
		}
	}
}
