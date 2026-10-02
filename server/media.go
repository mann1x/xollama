package server

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/types/xollama"
)

// A model's media engines (plans/media-integration.md) are served by a
// media-only engine process, scheduled as a runner of its own: the model's
// media twin. Its key is mediaKeyPrefix plus the model's manifest digest, so
// it never collides with the LLM's runner, and an edited template is a new
// runner while the old one expires.
const mediaKeyPrefix = "media:"

// Media capabilities, as /api/show reports them. Upstream's "image" is NOT
// one of them: the generate and chat paths refuse every model that has it,
// and a model with an LLM and an image engine must keep chatting.
const (
	CapabilityImageGeneration = model.Capability("image_generation")
	CapabilityImageEdit       = model.Capability("image_edit")
	CapabilitySpeech          = model.Capability("speech")
	CapabilityTranscription   = model.Capability("transcription")
	CapabilityVideo           = model.Capability("video")
)

func isMediaKey(path string) bool { return strings.HasPrefix(path, mediaKeyPrefix) }

func modelMedia(m *Model) *xollama.Media {
	if m == nil || m.Xollama == nil || m.Xollama.Media.IsZero() {
		return nil
	}
	return m.Xollama.Media
}

// mediaCapabilities lists what the model's media serves.
//
// xollama-hook: media (called from Model.Capabilities)
func mediaCapabilities(m *Model) []model.Capability {
	media := modelMedia(m)
	if media == nil {
		return nil
	}
	var out []model.Capability
	if media.Image != nil {
		out = append(out, CapabilityImageGeneration)
		if media.Image.Edit != "none" {
			out = append(out, CapabilityImageEdit)
		}
	}
	if media.STT != nil {
		out = append(out, CapabilityTranscription)
	}
	if media.TTS != nil {
		out = append(out, CapabilitySpeech)
	}
	if media.Video != nil {
		out = append(out, CapabilityVideo)
	}
	return out
}

// mediaTwin is the model the scheduler loads for its media. It is built
// fresh, not copied: the twin carries no GGUF metadata, template or weights,
// so nothing in the scheduler mistakes it for the LLM.
func mediaTwin(m *Model) *Model {
	return &Model{
		Name:      m.Name,
		ShortName: m.ShortName,
		Digest:    m.Digest,
		ModelPath: mediaKeyPrefix + m.Digest,
		Xollama:   &xollama.Config{Version: m.Xollama.Version, Media: m.Xollama.Media},
		// Neither GGUF nor safetensors: the capability walk then reads no
		// weights' metadata, and the scheduler sees no completion model.
		Config: model.ConfigV2{ModelFormat: "media"},
	}
}

func blobPath(digest string) string {
	p, err := manifest.BlobsPath(digest)
	if err != nil {
		return ""
	}
	return p
}

func blobSize(digest string) int64 {
	fi, err := os.Stat(blobPath(digest))
	if err != nil {
		return 0
	}
	return fi.Size()
}

// newMediaRunnerFn is the scheduler's constructor for a media twin; tests
// swap it, as they swap Scheduler.newServerFn.
//
// xollama-hook: media (called from Scheduler.load)
var newMediaRunnerFn = newMediaRunner

func newMediaRunner(m *Model) (llm.LlamaServer, error) {
	media := modelMedia(m)
	if media == nil {
		return nil, fmt.Errorf("%s has no media", m.ShortName)
	}
	var voices string
	if media.TTS != nil && media.TTS.Voices != "" {
		var err error
		if voices, err = mediaVoicesDir(media.TTS.Voices); err != nil {
			return nil, fmt.Errorf("voices of %s: %w", m.ShortName, err)
		}
	}
	return llm.NewMediaRunner(m.ModelPath, media, blobPath, blobSize, voices)
}

// mediaVoicesDir unpacks a voices tar once into the model store, next to the
// blobs, and returns the directory the engine reads (--tts-voices). The
// directory is named by the tar's digest, so a second model with the same
// voices shares it and a changed tar is a new directory.
func mediaVoicesDir(digest string) (string, error) {
	dir := filepath.Join(envconfig.Models(), "media", "voices", strings.ReplaceAll(digest, ":", "-"))
	done := filepath.Join(dir, ".unpacked")
	if _, err := os.Stat(done); err == nil {
		return dir, nil
	}
	f, err := os.Open(blobPath(digest))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := fsowner.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read voices tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// Flattened to its base name: a voice is one file, and a path from
		// the archive never reaches outside the directory.
		name := filepath.Base(filepath.Clean("/" + h.Name))
		if name == "/" || name == "." || strings.HasPrefix(name, ".") {
			continue
		}
		out, err := fsowner.Create(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
	}
	if err := fsowner.WriteFile(done, nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// errNoMedia answers a media request to a model that has none of that kind.
var errNoMedia = errors.New("does not serve this media")

// mediaRunner schedules the model's media engine and returns it running.
// kind is the capability the request needs.
func (s *Server) mediaRunner(ctx context.Context, name string, kind model.Capability, keepAlive *api.Duration) (llm.MediaRunner, *Model, error) {
	m, err := GetModel(name)
	if err != nil {
		return nil, nil, err
	}
	has := false
	for _, c := range mediaCapabilities(m) {
		if c == kind {
			has = true
		}
	}
	if !has {
		return nil, nil, fmt.Errorf("%s %w (%s)", name, errNoMedia, kind)
	}
	twin := mediaTwin(m)
	opts, err := s.modelOptions(twin, nil)
	if err != nil {
		return nil, nil, err
	}
	runnerCh, errCh := s.sched.getRunner(ctx, twin, opts, keepAlive, false, false, nil)
	select {
	case r := <-runnerCh:
		mr, ok := r.llama.(llm.MediaRunner)
		if !ok {
			return nil, nil, fmt.Errorf("%s: the scheduler returned a runner that is not a media engine", name)
		}
		return mr, m, nil
	case err := <-errCh:
		return nil, nil, err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// mediaTemplateCreate reports whether a create with neither from nor files
// is a media-only template: one whose config carries media and nothing to
// load as weights.
//
// xollama-hook: media (called from CreateHandler)
func mediaTemplateCreate(r api.CreateRequest) bool {
	return r.Xollama != nil && !r.Xollama.Media.IsZero()
}
