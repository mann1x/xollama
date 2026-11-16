package server

import (
	"cmp"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

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
func logModelLoad(m *Model, f *gguf.Model, gpus []ml.DeviceInfo, opts api.Options, numParallel int, predicted uint64, batchAuto bool) {
	if m != nil {
		predictedLoads.Store(m.ModelPath, predicted)
	}
	for _, l := range modelLoadLog(m, f, gpus, opts, numParallel, predicted, batchAuto) {
		slog.Info(l.msg, l.attrs...)
	}
	warnSwallowedTemplate(m)
}

func modelLoadLog(m *Model, f *gguf.Model, gpus []ml.DeviceInfo, opts api.Options, numParallel int, predicted uint64, batchAuto bool) []loadLogLine {
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
		"num_batch", opts.NumBatch, "num_batch_from", batchSource(batchAuto), "predicted", format.HumanBytes2(predicted),
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

// batchSource says where a load's num_batch came from: the scheduler's
// automatic choice, or the model or the request (mail #2308: the automatic
// 2048 was only visible by comparing two lines).
func batchSource(auto bool) string {
	if auto {
		return "auto"
	}
	return "model or request"
}

// predictedLoads holds each load's prediction until the engine reports what
// it took. Keyed by the model's blob path, as the scheduler keys runners.
var predictedLoads sync.Map

// logPredictionMiss says, once the engine is up, when it took a fifth more
// GPU memory than the scheduler predicted. That gap decides placement, the
// automatic num_batch and eviction, and it was silent (mail #2308: predicted
// 14.9 GiB, took 20.5 GiB).
func logPredictionMiss(m *Model, llama interface{ MemorySize() (uint64, uint64) }) {
	if m == nil || llama == nil {
		return
	}
	_, took := llama.MemorySize()
	v, ok := predictedLoads.LoadAndDelete(m.ModelPath)
	if !ok {
		return
	}
	predicted := v.(uint64)
	if predicted == 0 || took <= predicted+predicted/5 {
		return
	}
	slog.Info("the load took more GPU memory than predicted", "model", cmp.Or(m.ShortName, m.Name),
		"predicted", format.HumanBytes2(predicted), "took", format.HumanBytes2(took),
		"over", fmt.Sprintf("%d%%", (took-predicted)*100/predicted))
}
