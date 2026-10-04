package server

import (
	"cmp"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/fs/gguf"
	"github.com/ollama/ollama/ml"
)

// The engine's own log names a load only by its blob path, so a log with
// several models in it cannot be read without the manifests beside it. These
// lines name the model before the engine starts, from what the scheduler has
// already parsed and decided: no file is read and nothing is computed for
// them. They are logged whichever engine serves the load (load-log hook).

type loadLogLine struct {
	msg   string
	attrs []any
}

// logModelLoad writes the lines for one load, just before the engine starts.
func logModelLoad(m *Model, f *gguf.Model, gpus []ml.DeviceInfo, opts api.Options, numParallel int, predicted uint64) {
	for _, l := range modelLoadLog(m, f, gpus, opts, numParallel, predicted) {
		slog.Info(l.msg, l.attrs...)
	}
}

func modelLoadLog(m *Model, f *gguf.Model, gpus []ml.DeviceInfo, opts api.Options, numParallel int, predicted uint64) []loadLogLine {
	if m == nil {
		return nil
	}
	name := cmp.Or(m.ShortName, m.Name, "unknown")
	who := []any{"model", name, "blob", filepath.Base(m.ModelPath)}
	if n := len(m.ModelShardPaths); n > 1 {
		who = append(who, "shards", n)
	}
	if m.DraftPath != "" {
		who = append(who, "drafter", filepath.Base(m.DraftPath))
	}
	if n := len(m.ProjectorPaths); n > 0 {
		who = append(who, "projectors", n)
	}
	if n := len(m.AdapterPaths); n > 0 {
		who = append(who, "adapters", n)
	}
	lines := []loadLogLine{{"loading model", who}}

	if f != nil {
		kv := f.KV()
		file := []any{"model", name, "family", kv.Architecture(), "quant", kv.FileType().String()}
		if n := kv.ParameterCount(); n > 0 {
			file = append(file, "params", format.HumanNumber(n))
		}
		file = append(file, "size", format.HumanBytes2(f.FileSize()), "layers", kv.BlockCount(),
			"embedding", kv.EmbeddingLength(), "heads", fmt.Sprintf("%d/%d", kv.HeadCountMax(), kv.HeadCountKVMin()))
		if experts := kv.Uint("expert_count"); experts > 0 {
			file = append(file, "experts", fmt.Sprintf("%d/%d", kv.Uint("expert_used_count"), experts))
		}
		if w := kv.Uint("attention.sliding_window"); w > 0 {
			file = append(file, "sliding_window", w)
		}
		file = append(file, "trained_ctx", kv.ContextLength())
		lines = append(lines, loadLogLine{"model file", file})
	}

	lines = append(lines, loadLogLine{"model placement", []any{
		"model", name, "devices", loadDevices(gpus), "num_ctx", opts.NumCtx, "parallel", numParallel,
		"num_batch", opts.NumBatch, "predicted", format.HumanBytes2(predicted),
	}})
	return lines
}

// loadDevices names the devices a load is placed on, as the engine's own
// log numbers them: backend and index, then the card.
func loadDevices(gpus []ml.DeviceInfo) string {
	if len(gpus) == 0 {
		return "cpu"
	}
	names := make([]string, 0, len(gpus))
	for _, g := range gpus {
		names = append(names, fmt.Sprintf("%s%s %s", g.Library, g.ID, cmp.Or(g.Description, g.Name)))
	}
	return strings.Join(names, ", ")
}
