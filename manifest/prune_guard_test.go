package manifest

import (
	"net"
	"os"
	"testing"

	"github.com/ollama/ollama/types/model"
)

// The loss this guards against, in the store's own terms: two models, one
// whose manifest the system will not open, and a prune naming that model's
// layer. The layer is still listed by a manifest nobody can read, so it
// stays.
func TestAnUnreadableManifestStopsThePrune(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	weights := writeManifestBlobForTest(t, []byte("the weights"))
	config := writeManifestBlobForTest(t, []byte("the config"))
	name := model.ParseName("unreadable")
	if err := WriteManifest(name, Layer{Digest: config}, []Layer{{Digest: weights}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(model.ParseName("bystander"), Layer{}, nil); err != nil {
		t.Fatal(err)
	}

	// A name that is there and that the system will not open: a socket. It is
	// the nearest thing here to a link the system refuses to follow.
	path, _, err := resolveManifestPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("cannot make an unopenable name here: %v", err)
	}
	defer l.Close()

	removed, err := RemoveUnreferencedBlobs(weights)
	if err != nil {
		t.Fatalf("the prune failed instead of standing down: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v while a manifest was unreadable", removed)
	}
	blob, err := BlobsPath(weights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("the weights are gone: %v", err)
	}

	// Once the name is gone the layer is nobody's, and the prune works again:
	// the guard must not hold the store's garbage forever.
	l.Close()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	removed, err = RemoveUnreferencedBlobs(weights)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %v, want the unlisted layer", removed)
	}
}

// A name whose manifest is missing protects nothing, as upstream has it.
func TestAMissingManifestDoesNotStopThePrune(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	weights := writeManifestBlobForTest(t, []byte("the weights"))
	name := model.ParseName("dangling")
	if err := WriteManifest(name, Layer{}, []Layer{{Digest: weights}}); err != nil {
		t.Fatal(err)
	}
	path, _, err := resolveManifestPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".gone", path); err != nil {
		t.Skipf("cannot make a dangling name here: %v", err)
	}

	removed, err := RemoveUnreferencedBlobs(weights)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %v, want the layer of a manifest that is not there", removed)
	}
}
