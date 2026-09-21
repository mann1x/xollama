package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// payloadOwner identifies the artifact that unpacked the current payload.
//
// Size and modification time are the cheap half and carry the common case: a
// re-cut downloaded over an old file changes both. SHA256 is the half that is
// actually true, and it is computed only when the cheap half already disagrees
// -- so the steady state costs one stat, and hashing 700 MB happens on the
// launch after the artifact changed, not on every launch.
type payloadOwner struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"mtime_unix_nano"`
}

// DefaultPayloadRoot returns the directory xollama gives the engine as HOME.
//
// It is under ollama's own directory, never the user's ~/.llamafile, which is
// the point: the operator may run any number of opencoti builds themselves and
// must not have to think about ours.
func DefaultPayloadRoot(libOllamaPath, home string) string {
	if home != "" {
		return filepath.Join(home, ".ollama", "engines", "payload")
	}
	if libOllamaPath != "" {
		return filepath.Join(libOllamaPath, "engines", "payload")
	}
	return ""
}

// PreparePayloadHome makes root fit to be the engine's HOME and returns it.
//
// If the payload in root belongs to a different artifact than the one about to
// run, it is deleted so the engine unpacks its own. The returned path is what
// the caller sets HOME to; "" means keep the inherited environment, which is
// what happens when the root cannot be used at all.
//
// An unusable root is a warning and not an error. Refusing to launch would
// turn a cosmetic problem -- a directory we cannot write -- into a model that
// will not load, and the behaviour without this is exactly what xollama did
// before it existed.
func PreparePayloadHome(artifact, root string) string {
	if artifact == "" || root == "" {
		return ""
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		slog.Warn("cannot use a private directory for the engine payload; falling back to the inherited HOME",
			"root", root, "error", err,
			"consequence", "the engine shares ~/.llamafile with any other opencoti build on this machine")
		return ""
	}

	want, err := describeArtifact(artifact)
	if err != nil {
		slog.Warn("cannot identify the engine artifact; leaving the payload directory alone", "artifact", artifact, "error", err)
		return ""
	}

	markerPath := filepath.Join(root, payloadMarker)
	have, haveErr := readPayloadOwner(markerPath)

	switch {
	case haveErr == nil && have.sameFileAs(want):
		// Same artifact, untouched since we last looked. Nothing to do, and
		// nothing hashed.
		return root
	case haveErr == nil && have.SHA256 != "" && have.SHA256 == digestOf(artifact, want):
		// Stat changed but the bytes did not -- a re-download of the same
		// build, or a copy that moved the mtime. Keep the payload, refresh
		// what we recorded about it.
		want.SHA256 = have.SHA256
	default:
		// Either nothing is recorded, or a different artifact owns what is
		// there. Take the payload out before the engine can find it.
		want.SHA256 = digestOf(artifact, want)
		tree := filepath.Join(root, payloadDirName)
		if _, statErr := os.Stat(tree); statErr == nil {
			if err := os.RemoveAll(tree); err != nil {
				slog.Warn("could not purge the previous engine payload", "path", tree, "error", err)
				return ""
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
	return root
}

// sameFileAs reports whether nothing about the artifact has changed since the
// payload was unpacked. Path is part of it: two artifacts of equal size and
// timestamp in different places are still two artifacts.
func (p payloadOwner) sameFileAs(other payloadOwner) bool {
	return p.Artifact == other.Artifact && p.Size == other.Size && p.ModTime == other.ModTime
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
func digestOf(artifact string, _ payloadOwner) string {
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
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
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
