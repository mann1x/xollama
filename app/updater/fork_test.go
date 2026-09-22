//go:build windows || darwin

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/app/version"
)

// feed serves a GitHub-shaped release listing plus the assets it names, so the
// tests exercise the same two round trips the real feed does.
type feed struct {
	t        *testing.T
	releases []forkRelease
	bodies   map[string][]byte // asset name -> bytes
	srv      *httptest.Server
}

func newFeed(t *testing.T) *feed {
	t.Helper()
	f := &feed{t: t, bodies: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); !strings.Contains(got, "github") {
			t.Errorf("Accept = %q, want the GitHub media type", got)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("the update check must not send an Authorization header")
		}
		if r.URL.RawQuery != "" && !strings.Contains(r.URL.RawQuery, "per_page") {
			t.Errorf("unexpected query on the update check: %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(f.releases)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.bodies[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// release adds one release whose installer asset holds the given bytes, with a
// matching sha256sum.txt in the same release.
func (f *feed) release(tag string, prerelease bool, installerBody []byte, opts ...func(*forkRelease)) *feed {
	f.t.Helper()
	sum := sha256.Sum256(installerBody)
	sums := fmt.Sprintf("%s  ./%s\n%s  ./some-other-asset.zip\n",
		hex.EncodeToString(sum[:]), Installer, strings.Repeat("0", 64))

	f.bodies[tag+"-"+Installer] = installerBody
	f.bodies[tag+"-"+forkChecksumAsset] = []byte(sums)

	rel := forkRelease{
		TagName:    tag,
		Prerelease: prerelease,
		Assets: []forkAsset{
			{Name: Installer, URL: f.srv.URL + "/assets/" + tag + "-" + Installer, Size: int64(len(installerBody))},
			{Name: forkChecksumAsset, URL: f.srv.URL + "/assets/" + tag + "-" + forkChecksumAsset, Size: int64(len(sums))},
		},
	}
	for _, o := range opts {
		o(&rel)
	}
	f.releases = append(f.releases, rel)
	return f
}

func (f *feed) use(t *testing.T) {
	t.Helper()
	old := ReleaseFeedURL
	ReleaseFeedURL = f.srv.URL + "/releases?per_page=10"
	t.Cleanup(func() { ReleaseFeedURL = old })
}

func atVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func TestTheFeedOffersOnlyAReleaseNewerThanThisBuild(t *testing.T) {
	f := newFeed(t).
		release("v0.34.1", false, []byte("older")).
		release("v0.34.3", false, []byte("newer"))
	f.use(t)
	atVersion(t, "0.34.2")

	available, resp := checkForkUpdate(t.Context(), &Updater{})
	if !available {
		t.Fatal("expected v0.34.3 to be offered")
	}
	if resp.UpdateVersion != "v0.34.3" {
		t.Errorf("UpdateVersion = %q, want v0.34.3", resp.UpdateVersion)
	}
}

// The release job publishes every release as --draft --prerelease, so the
// default of refusing a pre-release is the difference between "updates when the
// owner promotes a build" and "updates the moment CI finishes".
func TestAPrereleaseIsRefusedUnlessAskedFor(t *testing.T) {
	f := newFeed(t).release("v0.35.0", true, []byte("candidate"))
	f.use(t)
	atVersion(t, "0.34.2")

	if available, _ := checkForkUpdate(t.Context(), &Updater{}); available {
		t.Fatal("a pre-release must not be offered by default")
	}

	old := AllowPrerelease
	AllowPrerelease = true
	t.Cleanup(func() { AllowPrerelease = old })

	available, resp := checkForkUpdate(t.Context(), &Updater{})
	if !available || resp.UpdateVersion != "v0.35.0" {
		t.Fatalf("with the pre-release channel on: available=%v version=%q", available, resp.UpdateVersion)
	}
}

func TestTheSameVersionIsNotAnUpdate(t *testing.T) {
	f := newFeed(t).release("v0.34.2", false, []byte("same"))
	f.use(t)
	atVersion(t, "0.34.2")

	if available, _ := checkForkUpdate(t.Context(), &Updater{}); available {
		t.Fatal("the version already installed must not be offered")
	}
}

// A release that carries no installer for this platform is not an update for
// this platform, however new it is.
func TestAReleaseWithoutThisPlatformsInstallerIsSkipped(t *testing.T) {
	f := newFeed(t).release("v0.35.0", false, []byte("x"), func(r *forkRelease) {
		for i := range r.Assets {
			if r.Assets[i].Name == Installer {
				r.Assets[i].Name = "something-else"
			}
		}
	})
	f.use(t)
	atVersion(t, "0.34.2")

	if available, _ := checkForkUpdate(t.Context(), &Updater{}); available {
		t.Fatal("a release with no installer for this platform must not be offered")
	}
}

// Without a checksum there is no way to tell our installer from anyone else's,
// which is the whole point of the feed. Refusing costs an update; accepting
// costs the guarantee.
func TestAReleaseWithNoChecksumFileIsRefused(t *testing.T) {
	f := newFeed(t).release("v0.35.0", false, []byte("x"), func(r *forkRelease) {
		kept := r.Assets[:0]
		for _, a := range r.Assets {
			if a.Name != forkChecksumAsset {
				kept = append(kept, a)
			}
		}
		r.Assets = kept
	})
	f.use(t)
	atVersion(t, "0.34.2")

	if available, _ := checkForkUpdate(t.Context(), &Updater{}); available {
		t.Fatal("a release with no sha256sum.txt must not be offered")
	}
}

func TestTheDigestGateAcceptsOurBytesAndRejectsAnybodyElses(t *testing.T) {
	ours := []byte("this is the xollama installer")
	f := newFeed(t).release("v0.35.0", false, ours)
	f.use(t)
	atVersion(t, "0.34.2")

	if available, _ := checkForkUpdate(t.Context(), &Updater{}); !available {
		t.Fatal("expected an update to be offered")
	}

	staged := filepath.Join(t.TempDir(), Installer)
	if err := os.WriteFile(staged, ours, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := forkDigest(staged); err != nil {
		t.Fatalf("our own bytes were rejected: %v", err)
	}

	// The case the upstream signer check waves through: a real, correctly
	// signed installer for a DIFFERENT product.
	if err := os.WriteFile(staged, []byte("this is the stock ollama installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := forkDigest(staged)
	if err == nil {
		t.Fatal("a different product's installer passed the digest gate")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
}

// The staged filename comes from content-disposition, so the feed controls it.
// It must not be able to steer the bytes away from the digest we recorded.
func TestADigestIsNotAcceptedForADifferentFilename(t *testing.T) {
	body := []byte("payload")
	f := newFeed(t).release("v0.35.0", false, body)
	f.use(t)
	atVersion(t, "0.34.2")
	if available, _ := checkForkUpdate(t.Context(), &Updater{}); !available {
		t.Fatal("expected an update to be offered")
	}

	staged := filepath.Join(t.TempDir(), "OllamaSetup.exe")
	if err := os.WriteFile(staged, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := forkDigest(staged); err == nil {
		t.Fatal("a digest recorded for one asset was accepted for another filename")
	}
}

func TestNothingIsInstalledBeforeAnyCheckHasRecordedADigest(t *testing.T) {
	expected.Lock()
	expected.name, expected.digest = "", ""
	expected.Unlock()

	staged := filepath.Join(t.TempDir(), Installer)
	if err := os.WriteFile(staged, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := forkDigest(staged); err == nil {
		t.Fatal("a download was accepted with no checksum on file")
	}
}

func TestChecksumLinesAreParsedTheWayTheReleaseJobWritesThem(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, tt := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"sha256sum over a directory", want + "  ./xOllamaSetup.exe\n", true},
		{"bare name", want + "  xOllamaSetup.exe\n", true},
		{"binary mode star", want + " *xOllamaSetup.exe\n", true},
		{"not present", want + "  other.exe\n", false},
		{"not a sha256", "deadbeef  ./xOllamaSetup.exe\n", false},
		{"not hex", strings.Repeat("z", 64) + "  ./xOllamaSetup.exe\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChecksums(strings.NewReader(tt.in), "xOllamaSetup.exe")
			if tt.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != want {
					t.Errorf("digest = %q, want %q", got, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
		})
	}
}

// The feed being unreachable is a non-event: the app keeps running on the
// version it has.
func TestAnUnreachableFeedIsNotAnUpdate(t *testing.T) {
	old := ReleaseFeedURL
	ReleaseFeedURL = "http://127.0.0.1:1/releases"
	t.Cleanup(func() { ReleaseFeedURL = old })

	if available, _ := checkForkUpdate(context.Background(), &Updater{}); available {
		t.Fatal("an unreachable feed must not report an update")
	}
}

// The fork must never be pointed back at upstream by accident.
func TestTheDefaultFeedIsTheForksOwnReleases(t *testing.T) {
	if strings.Contains(forkReleasesURL, "ollama.com") {
		t.Fatalf("the default update feed is upstream's: %s", forkReleasesURL)
	}
	if !strings.Contains(forkReleasesURL, "mann1x/xollama") {
		t.Fatalf("the default update feed does not name this fork: %s", forkReleasesURL)
	}
}

// A shipped build never moves UpdateCheckURLBase, so the fork feed must be
// what it uses. If this ever reads false in production, every install quietly
// goes back to asking ollama.com.
func TestAShippedBuildUsesTheForkFeed(t *testing.T) {
	if !forkFeedActive() {
		t.Fatalf("with UpdateCheckURLBase at its default (%q) the fork feed is not active", UpdateCheckURLBase)
	}
}

// The predicate compares against a copy of upstream's default. An upstream
// sync that moves that endpoint must fail here rather than leave the fork
// following upstream to a new address.
func TestTheUpstreamEndpointConstantIsStillUpstreamsDefault(t *testing.T) {
	if UpdateCheckURLBase != upstreamUpdateCheckURL {
		t.Fatalf("updater.go now defaults to %q but fork.go still guards %q; "+
			"reconcile them or the fork will follow upstream's new endpoint",
			UpdateCheckURLBase, upstreamUpdateCheckURL)
	}
}

// Pointing it anywhere else is how upstream's own tests keep exercising the
// endpoint protocol below the hook.
func TestAnExplicitEndpointTurnsTheForkFeedOff(t *testing.T) {
	old := UpdateCheckURLBase
	UpdateCheckURLBase = "http://127.0.0.1:0/update.json"
	t.Cleanup(func() { UpdateCheckURLBase = old })
	if forkFeedActive() {
		t.Fatal("an explicitly set endpoint must fall through to the upstream path")
	}
}
