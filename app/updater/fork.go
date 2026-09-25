//go:build windows || darwin

package updater

// The fork's update feed.
//
// Upstream asks https://ollama.com/api/update, signs the request with the
// local SSH key and installs whatever that endpoint hands back, gated only on
// the download being Authenticode-signed by "Ollama Inc." -- see
// updater_windows.go. On a fork every part of that is wrong in the same
// direction: the endpoint answers for Ollama, the signature check PASSES for
// Ollama's own installer and FAILS for ours, and the result is a silent
// downgrade of xollama to stock ollama on an hourly timer. The fork has already
// been bitten once by an updater it did not own (the Microsoft Store driving
// winget), and that one at least was outside the binary. This one is inside it.
//
// So the feed is the fork's own GitHub releases, and the gate is the sha256 the
// release itself publishes -- see forkDigest. Three deliberate differences from
// upstream:
//
//  1. NOTHING IDENTIFYING IS SENT. Upstream adds os, arch, version, a timestamp
//     and (on darwin) the install's device id, then signs the query with the
//     user's own key. A release listing is a static document on someone else's
//     server; asking for it with a signature attached leaks a stable identifier
//     for no answer it would change.
//
//  2. THE RELEASE IS CHOSEN HERE, not by the server. The release job creates
//     every release as `--draft --prerelease`, so /releases/latest -- which
//     skips both -- would never see one. We list releases and pick the newest
//     one this build is allowed to take, which also gives a pre-release channel
//     for free rather than as a second endpoint.
//
//  3. THE ANSWER IS NOT TRUSTED. checkForkUpdate records the sha256 that the
//     release's own sha256sum.txt gives for the asset it points at, and the
//     download is rejected unless the bytes match it. A feed that offered a
//     different product's installer would fail here even if it were signed,
//     which is precisely the case upstream's signer check waves through.

import (
	"bufio"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/ollama/ollama/app/version"
	"github.com/ollama/ollama/envconfig"
)

const (
	// forkReleasesURL lists the fork's releases, newest first. It is the
	// list endpoint and not /releases/latest on purpose; see (2) above.
	forkReleasesURL = "https://api.github.com/repos/mann1x/xollama/releases?per_page=10"

	// upstreamUpdateCheckURL is the value updater.go declares for
	// UpdateCheckURLBase. It is repeated here rather than referenced so that an
	// upstream sync which CHANGES that endpoint shows up as this file's test
	// failing, instead of as the fork silently following upstream somewhere new.
	upstreamUpdateCheckURL = "https://ollama.com/api/update"

	// forkPayloadIDAsset carries the digest of the payload the release's full
	// installer ships, so an install can tell whether it already has those
	// bytes without downloading them to find out.
	forkPayloadIDAsset = "payload-id.txt"

	// forkChecksumAsset is written by the release job:
	//   find . -type f -not -name 'sha256sum.txt' | xargs sha256sum
	// so every line is "<64 hex>  ./<name>".
	forkChecksumAsset = "sha256sum.txt"
)

var (
	// ReleaseFeedURL is where updates are looked for. XOLLAMA_UPDATE_FEED
	// redirects it, which is how the tests point it at a local server and how
	// an operator can pin a private mirror. It is read once, at startup,
	// because an update feed that can change under a running process is a
	// worse property than one that needs a restart.
	ReleaseFeedURL = cmp.Or(envconfig.UpdateFeed(), forkReleasesURL)

	// AllowPrerelease lets this build take a release marked pre-release.
	// Every release the job creates starts that way, so a build that refuses
	// them will simply never update until someone promotes one -- which is the
	// intended default, not an oversight.
	AllowPrerelease = envconfig.UpdatePrerelease()

	// CoreInstaller is the executables-only installer, published beside the
	// full one and taken when the installed payload already matches the
	// release's. Empty where there is no such split -- macOS ships one bundle.
	// Set by fork_payload_<os>.go.
	CoreInstaller string

	// InstalledPayloadID reports the payload digest this install is running
	// against, or "" when it cannot be told. Unknown always costs a full
	// download and never a wrong one. Set by fork_payload_<os>.go.
	InstalledPayloadID = func() string { return "" }

	// expected is the digest the feed published for the asset the next
	// download will fetch. It is set by checkForkUpdate and read by
	// forkDigest, which run on the same goroutine in the background checker;
	// the lock is for TriggerImmediateCheck racing a download in flight.
	expected struct {
		sync.Mutex
		name   string
		digest string
	}
)

type forkAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type forkRelease struct {
	TagName    string      `json:"tag_name"`
	Draft      bool        `json:"draft"`
	Prerelease bool        `json:"prerelease"`
	Assets     []forkAsset `json:"assets"`
}

// forkFeedActive reports whether the update path should use the fork's feed.
// It is false only when something has deliberately pointed UpdateCheckURLBase
// elsewhere, which in this tree is exactly upstream's own tests: they stand up
// a local server and exercise the endpoint protocol, and keeping that working
// keeps the next upstream sync a readable diff. A shipped build never moves it.
func forkFeedActive() bool {
	return UpdateCheckURLBase == upstreamUpdateCheckURL
}

// checkForkUpdate replaces Updater.checkForUpdate on this fork. The signature
// matches so the hook in updater.go is one line.
func checkForkUpdate(ctx context.Context, _ *Updater) (bool, UpdateResponse) {
	var none UpdateResponse

	releases, err := fetchForkReleases(ctx)
	if err != nil {
		slog.Warn("failed to check for update", "feed", ReleaseFeedURL, "error", err)
		return false, none
	}

	rel, asset := pickForkRelease(releases, Installer, AllowPrerelease)
	if rel == nil {
		slog.Debug("no update available", "current", version.Version, "feed", ReleaseFeedURL)
		return false, none
	}

	// A release that changed nothing under lib\ollama can be installed by the
	// executables-only installer, which is the whole delta mechanism: no diffs,
	// no patch format, no bespoke file replacer -- the same Inno installer that
	// already knows how to stop the tray app, rewrite PATH and keep the
	// uninstall entry, carrying two thirds less.
	if core, ok := chooseCoreInstaller(ctx, rel); ok {
		slog.Info("payload unchanged; taking the executables-only installer",
			"asset", core.Name, "instead_of", asset.Name,
			"saved_bytes", asset.Size-core.Size)
		asset = core
	}

	digest, err := fetchForkDigest(ctx, rel, asset.Name)
	if err != nil {
		// No digest, no install. Refusing here costs the user an update they
		// could have had; taking it costs them the one guarantee this feed has.
		slog.Warn("refusing an update with no published checksum",
			"version", rel.TagName, "asset", asset.Name, "error", err)
		return false, none
	}

	expected.Lock()
	expected.name, expected.digest = asset.Name, digest
	expected.Unlock()

	slog.Info("new update available", "version", rel.TagName, "asset", asset.Name, "url", asset.URL)
	return true, UpdateResponse{UpdateURL: asset.URL, UpdateVersion: rel.TagName}
}

func fetchForkReleases(ctx context.Context) ([]forkRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ReleaseFeedURL, nil)
	if err != nil {
		return nil, err
	}
	forkRequestHeaders(req)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("update feed returned %d: %.256s", resp.StatusCode, body)
	}

	var releases []forkRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("malformed update feed: %w", err)
	}
	return releases, nil
}

// forkRequestHeaders carries no identity: no signature, no device id, no
// version. See (1) at the top of this file.
func forkRequestHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", fmt.Sprintf("xollama/%s %s", version.Version, runtime.GOARCH))
}

// pickForkRelease returns the newest release this build may take together with
// the asset to install, or nil when there is nothing newer. "Newer" is a semver
// comparison and never a string inequality: a feed that answers with the
// version already installed, or with an older one after a release is pulled,
// must not start an install.
func pickForkRelease(releases []forkRelease, installer string, allowPrerelease bool) (*forkRelease, forkAsset) {
	current := semverOf(version.Version)

	var best *forkRelease
	var bestAsset forkAsset
	for i := range releases {
		rel := &releases[i]
		if rel.Draft {
			continue
		}
		if rel.Prerelease && !allowPrerelease {
			continue
		}
		candidate := semverOf(rel.TagName)
		if !semver.IsValid(candidate) || semver.Compare(candidate, current) <= 0 {
			continue
		}
		asset, ok := pickForkAsset(rel.Assets, installer)
		if !ok {
			// A release that does not carry this platform's installer is not
			// an update for this platform, however new it is.
			slog.Debug("release carries no installer for this platform",
				"version", rel.TagName, "installer", installer)
			continue
		}
		if best == nil || semver.Compare(candidate, semverOf(best.TagName)) > 0 {
			best, bestAsset = rel, asset
		}
	}
	return best, bestAsset
}

// pickForkAsset matches by exact name. Installer is already the per-platform
// artifact name that updater_windows.go and updater_darwin.go set at init, and
// the release job publishes it under exactly that name -- so matching on it is
// the same statement as "the asset for this platform", written once.
func pickForkAsset(assets []forkAsset, installer string) (forkAsset, bool) {
	for _, a := range assets {
		if a.Name == installer {
			return a, true
		}
	}
	return forkAsset{}, false
}

// semverOf normalises a release tag or a build version into something
// semver.Compare will accept: tags are "v0.34.2", version.Version is "0.34.2",
// and a dev build is "0.0.0".
func semverOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	return s
}

// normalisePayloadID accepts only a bare sha256. Anything else reads as
// unknown, which costs a full download and never a wrong one.
func normalisePayloadID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return ""
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return s
}

// fetchForkAssetText downloads one small text asset. Release assets need the
// octet-stream Accept or the API answers with JSON metadata instead of bytes.
func fetchForkAssetText(ctx context.Context, asset forkAsset, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	forkRequestHeaders(req)
	req.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %d", asset.Name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// fetchForkDigest reads the sha256 the release published for one asset. The
// checksum file is taken from THE SAME release as the asset, so a release
// cannot hand out a checksum from another one.
func fetchForkDigest(ctx context.Context, rel *forkRelease, assetName string) (string, error) {
	sums, ok := pickForkAsset(rel.Assets, forkChecksumAsset)
	if !ok {
		return "", fmt.Errorf("release %s publishes no %s", rel.TagName, forkChecksumAsset)
	}

	body, err := fetchForkAssetText(ctx, sums, 1<<20)
	if err != nil {
		return "", err
	}
	return parseChecksums(strings.NewReader(body), assetName)
}

// parseChecksums finds one name in `sha256sum` output. The release job runs it
// over a directory, so every path is "./<name>"; accept both spellings rather
// than depend on which.
func parseChecksums(r io.Reader, want string) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		digest, name := fields[0], path.Base(strings.TrimPrefix(fields[1], "*"))
		if name != want {
			continue
		}
		if len(digest) != sha256.Size*2 {
			return "", fmt.Errorf("checksum for %s is not a sha256: %.16s", want, digest)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("checksum for %s is not hex: %w", want, err)
		}
		return strings.ToLower(digest), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum for %s", want)
}

// chooseCoreInstaller returns the executables-only installer when this install
// already has the payload the release was built with. Every way of not knowing
// -- no core installer on this platform, no marker on disk, no payload-id.txt
// in the release, a mismatch -- falls back to the full installer, which is
// always correct and only ever costs bytes.
func chooseCoreInstaller(ctx context.Context, rel *forkRelease) (forkAsset, bool) {
	if CoreInstaller == "" {
		return forkAsset{}, false
	}
	core, ok := pickForkAsset(rel.Assets, CoreInstaller)
	if !ok {
		return forkAsset{}, false
	}
	installed := InstalledPayloadID()
	if installed == "" {
		slog.Debug("no payload marker on disk; taking the full installer")
		return forkAsset{}, false
	}
	published, err := fetchForkPayloadID(ctx, rel)
	if err != nil || published == "" {
		slog.Debug("release publishes no payload id; taking the full installer", "error", err)
		return forkAsset{}, false
	}
	if published != installed {
		slog.Info("payload changed in this release; taking the full installer",
			"installed", installed[:12], "release", published[:12])
		return forkAsset{}, false
	}
	return core, true
}

func fetchForkPayloadID(ctx context.Context, rel *forkRelease) (string, error) {
	asset, ok := pickForkAsset(rel.Assets, forkPayloadIDAsset)
	if !ok {
		return "", fmt.Errorf("release %s publishes no %s", rel.TagName, forkPayloadIDAsset)
	}
	body, err := fetchForkAssetText(ctx, asset, 4<<10)
	if err != nil {
		return "", err
	}
	id := normalisePayloadID(body)
	if id == "" {
		return "", fmt.Errorf("%s is not a sha256", forkPayloadIDAsset)
	}
	return id, nil
}

// forkDigest is the gate the whole feed rests on: the staged bytes must hash to
// what the release said they would. It runs before the platform's own
// VerifyDownload, so a payload that is not ours is rejected before anything
// looks at a signature -- which is the failure upstream's signer check cannot
// see, because a stock ollama installer really is signed by Ollama Inc.
func forkDigest(staged string) error {
	expected.Lock()
	wantName, wantDigest := expected.name, expected.digest
	expected.Unlock()

	if wantDigest == "" {
		return fmt.Errorf("no published checksum for %s; refusing to install it", path.Base(staged))
	}
	// The staged name is whatever content-disposition gave, or the platform
	// default when the server sent none -- those are the only two DownloadNewRelease
	// can produce, and with two installers per release the asset name is no
	// longer always the default. Anything else means the digest on file and the
	// bytes on disk came from different steps.
	if got := path.Base(strings.ReplaceAll(staged, `\`, `/`)); got != wantName && got != Installer {
		return fmt.Errorf("staged %s but the checksum on file is for %s", got, wantName)
	}

	f, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != wantDigest {
		return fmt.Errorf("checksum mismatch for %s: got %s, release published %s", wantName, sum, wantDigest)
	}
	slog.Debug("update checksum verified", "asset", wantName, "sha256", wantDigest)
	return nil
}
