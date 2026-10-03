package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
)

// Where the engine unpacks its GPU payload, and why xollama picks the place.
//
// A release artifact carries its ggml-*.so inside itself and unpacks them on
// first run to
//
//	$HOME/.llamafile/v/<engine-version>/
//
// Three segments, and only one of them is ours. The ".llamafile" segment comes
// from llamafile's own g_app_name and is settable only through an in-process C
// call. The version segment is compiled in -- "opencoti-" plus the llamafile
// version plus the cut tag -- so it cannot distinguish two builds carrying the
// same tag. $HOME is the whole of our influence, and it is enough.
//
// Why that matters. opencoti re-cuts a release IN PLACE: c7 r1 and c7 r2 are
// different bytes under one tag, so both resolve to the same directory. The
// engine only unpacks when what is already there is older, and then dlopens
// whatever it found -- so a host that has run r1 can go on running r1's CUDA
// kernels under an r2 binary, silently, until something overwrites them.
// opencoti already hit the coarser form of this (their bug-2272: c5, c6 and c7
// all landing in ~/.llamafile/v/0.10.3/) and namespaced by cut; that does not
// separate re-cuts of one cut, and cannot, because the tag is what stays equal.
// Measured on this host: one directory name held three different ggml-cuda.so
// inside 36 hours, and two users held two different ones at the same moment.
//
// So xollama stops sharing. The engine subprocess is given a HOME of our own,
// holding one payload -- the one belonging to the artifact about to run -- and
// anything a different artifact left there is deleted first. The user's own
// ~/.llamafile is then untouched: they can run whatever opencoti builds they
// like by hand, and none of it reaches, or is reached by, the engine we launch.
//
// This is deliberately not a cache of several payloads. Keeping one is what
// makes "which kernels are loaded" answerable without hashing anything at
// runtime, and it bounds the directory at one payload instead of one per
// artifact ever launched.

// payloadMarker records which artifact owns the payload currently unpacked in
// the root. It sits beside the .llamafile tree rather than inside it, so a
// purge of the tree never takes the record of what the tree was.
const payloadMarker = ".xollama-payload.json"

// payloadDirName is the subdirectory llamafile unpacks into, relative to the
// HOME it is given. Purging is scoped to exactly this, so a misconfigured root
// can never make xollama delete something it did not create.
const payloadDirName = ".llamafile"

// payloadLockName is the lock file in the root that two xollama processes take
// before either reads the marker or purges the tree.
const payloadLockName = ".xollama-payload.lock"

// payloadMu serializes the payload preparation within the process: an LLM
// engine and a media engine starting together would otherwise both read "no
// marker", and one could purge the tree the other's engine is already
// unpacking into. The root's file lock covers a second process.
var payloadMu sync.Mutex

// foreignWarned holds the roots already reported as holding another account's
// files, so the warning is said once per server and not once per engine run.
var foreignWarned sync.Map

// payloadOwner identifies the bytes that unpacked the current payload.
//
// Identity is content, not path: the same bytes at two paths own one payload.
// Size and modification time are the cheap half and carry the common case: a
// re-cut downloaded over an old file changes both. SHA256 is the half that is
// actually true, and it is computed only when no stat seen for these bytes
// matches -- so the steady state costs one stat, and hashing 700 MB happens
// once per new copy of the artifact, not on every launch.
type payloadOwner struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"mtime_unix_nano"`
	// Seen are the stats of every copy whose bytes hashed equal to SHA256,
	// so another path to the same bytes is known by its stat from then on.
	Seen []payloadStat `json:"seen,omitempty"`
}

// payloadStat is the cheap identity of one copy of the artifact.
type payloadStat struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mtime_unix_nano"`
}

// maxSeen bounds the stats one marker remembers.
const maxSeen = 8

// PayloadHome is PreparePayloadHome over the default roots: the HOME every
// run of the engine gets, whether it serves a model, lists devices or probes a
// link. One function, so a listing can never see a different HOME than the
// launch it is listing for.
func PayloadHome(artifact, libOllamaPath, home string) string {
	return PreparePayloadHome(artifact, DefaultPayloadRoots(libOllamaPath, home)...)
}

// HomeOf is the HOME a command's environment ends up with: the last one, as
// os/exec resolves duplicates.
func HomeOf(env []string) string {
	home := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	return home
}

// DefaultPayloadRoots returns the directories xollama will try to give the
// engine as HOME, best first.
//
// The first is ollama's own runtime directory -- the one already holding
// llama-server and the ggml backends, which is `<install>/lib/ollama` on every
// platform:
//
//	Windows  %LOCALAPPDATA%\Programs\Ollama\lib\ollama
//	Linux    /usr/local/lib/ollama
//	macOS    Ollama.app/Contents/Resources/lib/ollama
//
// That is where the engine's runtime belongs: beside the rest of the runtime,
// installed and removed with it, and in nobody's home directory.
//
// It is not always writable, and that is not a fault. A packaged Linux install
// leaves that directory root-owned while the service runs as `ollama`. So `~/.ollama/engines/payload` follows as a fallback -- still
// ours, and still never the user's `~/.llamafile`, which is the directory they
// keep their own opencoti builds in and the one this whole file exists to stay
// out of.
func DefaultPayloadRoots(libOllamaPath, home string) []string {
	return payloadRoots(runtime.GOOS, libOllamaPath, home)
}

// payloadRoots is DefaultPayloadRoots for one OS. macOS gets the home
// directory only: its runtime directory is inside the signed app bundle, a
// user-owned install leaves it writable, and a file written there breaks the
// bundle's signature.
func payloadRoots(goos, libOllamaPath, home string) []string {
	var roots []string
	if libOllamaPath != "" && goos != "darwin" {
		roots = append(roots, filepath.Join(libOllamaPath, "engines", "payload"))
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, ".ollama", "engines", "payload"))
	}
	return roots
}

// ensureWritable creates root if needed and proves we can write inside it.
//
// The proof is the point. The failing case on Linux is a directory that already
// exists and is owned by root, so MkdirAll succeeds and nothing goes wrong
// until the first write -- which, on the path that matters, happens after
// hashing 700 MB. Probing costs two syscalls and moves that discovery in front
// of the work.
//
// It is a var because that case cannot be built from a fixture: these tests run
// as root on the host that found the bug, and root is not stopped by a mode bit.
var ensureWritable = func(root string) error {
	if err := fsowner.MkdirAll(root, 0o755); err != nil {
		return err
	}
	// On a packaged Linux install the server runs as an unprivileged account
	// and an administrator's one-off root command must not leave a directory
	// that account cannot write. See internal/fsowner -- the same mistake
	// against the model store cost this setup a 165x slowdown that never
	// surfaced as an error.
	if owner, ok := fsowner.Intended(envconfig.Models()); ok {
		fsowner.AdoptQuietly(root, owner)
	}
	f, err := fsowner.CreateTemp(root, ".xollama-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// PreparePayloadHome makes the first usable root fit to be the engine's HOME
// and returns it, trying the roots in the order given.
//
// If the payload in that root belongs to a different artifact than the one
// about to run, it is deleted so the engine unpacks its own. The returned path
// is what the caller sets HOME to; "" means keep the inherited environment,
// which is what happens when no root can be used at all.
//
// An unusable root is a warning and not an error. Refusing to launch would turn
// a cosmetic problem -- a directory we cannot write -- into a model that will
// not load, and what happens instead is exactly what xollama did before any of
// this existed.
func PreparePayloadHome(artifact string, roots ...string) string {
	if artifact == "" {
		return ""
	}

	want, err := describeArtifact(artifact)
	if err != nil {
		// Nothing here is root-dependent -- the artifact is what we cannot
		// read -- so no other candidate would do better.
		slog.Warn("cannot identify the engine artifact; leaving the payload directory alone", "artifact", artifact, "error", err)
		return ""
	}

	for _, root := range roots {
		if root == "" {
			continue
		}
		if err := ensureWritable(root); err != nil {
			slog.Debug("cannot write to this engine payload directory; trying the next", "root", root, "error", err)
			continue
		}
		// Files an earlier run left as another account make the root one the
		// engine cannot work in, though the directory itself is writable.
		if err := payloadForeign(root); err != nil {
			// Once per root: every listing and every launch comes through here.
			level := slog.LevelDebug
			if _, told := foreignWarned.LoadOrStore(root, true); !told {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "the engine payload directory holds files this account cannot use; trying the next",
				"root", root, "error", err,
				"consequence", "with it as HOME the engine could not load its GPU library",
				"fix", "as root: chown -R <the account xollama runs as> "+root)
			continue
		}
		home, err := preparePayloadRoot(artifact, root, want)
		if err != nil {
			slog.Warn("could not prepare the engine payload directory; trying the next", "root", root, "error", err)
			continue
		}
		// As root, hand over what an earlier root run left before this engine
		// adds to it; the launch hands the rest over when the engine exits.
		AdoptPayloadHome(home)
		return home
	}

	slog.Warn("no private directory available for the engine payload; falling back to the inherited HOME",
		"roots", roots,
		"consequence", "the engine shares ~/.llamafile with any other opencoti build on this machine")
	return ""
}

// preparePayloadRoot is PreparePayloadHome for one already-writable root. It
// holds the process lock and the root's file lock throughout, so no other
// launch reads the marker or purges the tree in between.
func preparePayloadRoot(artifact, root string, want payloadOwner) (string, error) {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	unlock, err := lockPayloadRoot(filepath.Join(root, payloadLockName))
	if err != nil {
		return "", fmt.Errorf("locking the engine payload directory: %w", err)
	}
	defer unlock()

	markerPath := filepath.Join(root, payloadMarker)
	have, haveErr := readPayloadOwner(markerPath)

	switch {
	case haveErr == nil && have.sameFileAs(want):
		// The same bytes, by a stat already seen. Nothing to do, and nothing
		// hashed.
		return root, nil
	case haveErr == nil && have.SHA256 != "" && have.SHA256 == digestOf(artifact, want):
		// A stat not seen yet, but the same bytes -- a re-download of the
		// same build, or the same bytes at another path. Keep the payload and
		// remember this copy's stat.
		want.SHA256 = have.SHA256
		want.Seen = have.withSeen(want)
	default:
		// Either nothing is recorded, or a different artifact owns what is
		// there. Take the payload out before the engine can find it.
		want.SHA256 = digestOf(artifact, want)
		want.Seen = []payloadStat{want.stat()}
		tree := filepath.Join(root, payloadDirName)
		if _, statErr := os.Stat(tree); statErr == nil {
			if err := os.RemoveAll(tree); err != nil {
				return "", fmt.Errorf("purging the payload left by %s: %w", have.Artifact, err)
			}
			slog.Info("purged the engine payload left by a different artifact",
				"path", tree, "now_running", artifact,
				"previous_artifact", have.Artifact, "previous_sha256", shortSHA(have.SHA256))
		}
	}

	if err := writePayloadOwner(markerPath, want); err != nil {
		// The payload is correct; only the bookkeeping failed. Using the root
		// still isolates this launch -- the next one just cannot prove the
		// payload is its own and will purge again.
		slog.Warn("could not record which artifact owns the engine payload", "path", markerPath, "error", err)
	}
	return root, nil
}

// sameFileAs reports whether other's stat is one already seen for the bytes
// that own the payload. The path is not part of it: the same bytes may run
// from two places, and that must neither purge nor re-hash.
func (p payloadOwner) sameFileAs(other payloadOwner) bool {
	s := other.stat()
	return p.stat() == s || slices.Contains(p.Seen, s)
}

func (p payloadOwner) stat() payloadStat { return payloadStat{Size: p.Size, ModTime: p.ModTime} }

// withSeen is p's seen stats plus other's, newest last, at most maxSeen.
func (p payloadOwner) withSeen(other payloadOwner) []payloadStat {
	out := make([]payloadStat, 0, len(p.Seen)+2)
	for _, s := range append([]payloadStat{p.stat()}, p.Seen...) {
		if s != other.stat() && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	out = append(out, other.stat())
	if len(out) > maxSeen {
		out = out[len(out)-maxSeen:]
	}
	return out
}

func describeArtifact(artifact string) (payloadOwner, error) {
	fi, err := os.Stat(artifact)
	if err != nil {
		return payloadOwner{}, err
	}
	return payloadOwner{
		Artifact: artifact,
		Size:     fi.Size(),
		ModTime:  fi.ModTime().UnixNano(),
	}, nil
}

// digestOf hashes the artifact, returning "" when it cannot. A failure here
// costs a purge that was not needed, never a payload that should have gone.
//
// It is a var so a test can count the calls: whether the artifact is hashed
// before or after a root is known to be writable is invisible in the result and
// worth many seconds per model load, so it needs a test that can see it.
var digestOf = func(artifact string, _ payloadOwner) string {
	return digestArtifact(artifact)
}

func digestArtifact(artifact string) string {
	f, err := os.Open(artifact)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readPayloadOwner(path string) (payloadOwner, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return payloadOwner{}, err
	}
	var p payloadOwner
	if err := json.Unmarshal(b, &p); err != nil {
		return payloadOwner{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func writePayloadOwner(path string, p payloadOwner) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// The marker is read on the next launch, possibly by the service account
	// rather than by whoever wrote it: the wrapper hands the file over, and
	// the rename keeps its owner.
	if err := fsowner.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
