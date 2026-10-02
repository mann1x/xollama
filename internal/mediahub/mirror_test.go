package mediahub

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fileFor(content string) File {
	sum := sha256.Sum256([]byte(content))
	return File{
		Ref:    Ref{Repo: "owner/repo", Path: "sub/model.gguf", Rev: "main"},
		Digest: fmt.Sprintf("sha256:%x", sum), Size: int64(len(content)), Commit: "abc123",
	}
}

func hub(t *testing.T, body string) *atomic.Int32 {
	t.Helper()
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/owner/repo/resolve/abc123/sub/model.gguf" {
			t.Errorf("fetched %s, want the resolved commit's file", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		gets.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HF_ENDPOINT", srv.URL)
	return &gets
}

func TestFetchKeepsTheFileOnlyWhenItsDigestIsTheHubs(t *testing.T) {
	dir := t.TempDir()
	gets := hub(t, "the weights")
	f := fileFor("the weights")

	p, fetched, err := Fetch(t.Context(), http.DefaultClient, dir, f, nil)
	if err != nil || !fetched || p != filepath.Join(dir, "owner", "repo", "sub", "model.gguf") {
		t.Fatalf("fetch = %q %v %v", p, fetched, err)
	}
	if b, _ := os.ReadFile(p + ".sha256"); !strings.HasPrefix(string(b), f.Digest) {
		t.Fatalf("sidecar = %q", b)
	}

	// The second fetch finds it and downloads nothing.
	if _, fetched, err := Fetch(t.Context(), http.DefaultClient, dir, f, nil); err != nil || fetched || gets.Load() != 1 {
		t.Fatalf("second fetch: fetched %v, err %v, %d downloads", fetched, err, gets.Load())
	}

	// A download that is not the file the hub named never takes its place.
	other := t.TempDir()
	bad := fileFor("other weights")
	bad.Size = int64(len("the weights"))
	if _, _, err := Fetch(t.Context(), http.DefaultClient, other, bad, nil); err == nil {
		t.Fatal("a download with the wrong sha256 was accepted")
	}
	if _, err := os.Stat(MirrorPath(other, bad.Ref)); !os.IsNotExist(err) {
		t.Fatal("the wrong download was left in the mirror")
	}
}

func TestAMirroredFileIsTrustedByItsDigestNotItsName(t *testing.T) {
	dir := t.TempDir()
	f := fileFor("right")
	p := MirrorPath(dir, f.Ref)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Mirrored(dir, f); ok {
		t.Fatal("a file with the right name and size but another sha256 was trusted")
	}
	// Replaced by the right file of the same size: the sidecar recorded for
	// the old content must not stand for the new.
	if err := os.WriteFile(p, []byte("right"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if got, ok := Mirrored(dir, f); !ok || got != p {
		t.Fatalf("mirrored = %q %v", got, ok)
	}
	if _, ok := Mirrored("", f); ok {
		t.Fatal("no mirror dir found a file")
	}
}
