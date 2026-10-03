package server

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
	"github.com/ollama/ollama/types/xollama"
)

// audio.cpp (the TTS engine for Kokoro, Supertonic and KittenTTS) tells a
// model file's format by its name: a path without ".gguf" is read as a
// safetensors model package, and a blob path ("sha256-…") never has one. So
// the load failed on every published audio.cpp template, looking for
// config.json or voice_styles/ beside the blob (bs2, b112, 2026-10-03), where
// opencoti's own gate had passed "<file>.gguf". The other engines read the
// file's magic and take the blob path as it is. A symlink is not enough:
// audio.cpp resolves it and judges the blob's own name, so the name has to be
// the file's (a hard link).

// ggufMagic opens every GGUF file.
var ggufMagic = []byte("GGUF")

// mediaComponentPath maps a component digest to the path its engine is given:
// the blob, except an audio.cpp speech model in GGUF, which gets a .gguf name.
func mediaComponentPath(m *xollama.Media) func(string) string {
	return func(digest string) string {
		if t := m.TTS; t != nil && t.Engine == "audiocpp" && digest == t.Model {
			if p, ok := namedGGUF(digest); ok {
				return p
			}
		}
		return blobPath(digest)
	}
}

// namedGGUF hard-links <models>/media/named/<digest>.gguf to the blob, once,
// when the blob is a GGUF file. A link that outlives its blob would keep the
// space a removed model frees, so every call first drops the links whose blob
// is gone. Any failure leaves the caller on the blob path, which is no worse
// than before.
func namedGGUF(digest string) (string, bool) {
	blob := blobPath(digest)
	f, err := os.Open(blob)
	if err != nil {
		return "", false
	}
	head := make([]byte, len(ggufMagic))
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil || !bytes.Equal(head, ggufMagic) {
		return "", false
	}
	dir := filepath.Join(envconfig.Models(), "media", "named")
	dropOrphanNames(dir)
	link := filepath.Join(dir, strings.ReplaceAll(digest, ":", "-")+".gguf")
	bi, err := os.Stat(blob)
	if err != nil {
		return "", false
	}
	if li, err := os.Lstat(link); err == nil && os.SameFile(li, bi) {
		return link, true
	}
	if err := fsowner.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	_ = os.Remove(link)
	if err := os.Link(blob, link); err != nil {
		return "", false
	}
	return link, true
}

// dropOrphanNames removes the names whose blob is gone: the model was removed
// and the blob pruned, so the name is the file's last link.
func dropOrphanNames(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".gguf") {
			continue
		}
		digest := strings.Replace(strings.TrimSuffix(name, ".gguf"), "-", ":", 1)
		if _, err := os.Stat(blobPath(digest)); errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
