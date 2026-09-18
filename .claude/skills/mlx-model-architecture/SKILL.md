---
name: mlx-model-architecture
description: Adds a model architecture to the MLX runner as a Go package under mlxrunner/model/<name>/ that implements model.Model, reuses mlxrunner/nn layers and mlxrunner/cache slots, registers itself via model.Register in init(), gets a blank import in mlxrunner/model/architectures/architectures.go, and ships a sibling _test.go using mlx/mlxtest. Use when the user says 'add <model> to mlxrunner', 'support <arch>ForCausalLM', 'MLX architecture', 'safetensors model support', 'register a draft model', or edits anything under mlxrunner/model/, mlxrunner/nn/ or mlxrunner/cache/. Capabilities: config parsing and validation, quantization wiring via model.NewLinearFactory, weight-prefix resolution, KV/rotating/recurrent cache layout, attention and MLP forward passes, registration, and tests that skip cleanly where MLX is unavailable. Do NOT use for GGUF or llama-server work in llm/ (llm/server.go, llm/llama_server.go, llm/engine/), for prompt renderers/parsers in model/renderers/ or model/parsers/, for server/sched.go scheduling, or for tokenizer-only changes in mlxrunner/tokenizer/.
paths:
  - mlxrunner/model/**
  - mlxrunner/nn/**
  - mlxrunner/cache/**
  - mlxrunner/runner.go
---
# MLX Model Architecture

## Critical

- **The Go module path stays upstream's**, as declared in `go.mod`. A new package is imported through upstream's module path plus `mlxrunner/model/<name>`, never a rewritten xollama path. See CLAUDE.md rule 1.
- **`mlxrunner/` is upstream code.** The new package *directory* is additive and conflict-free. The one blank-import line you add to `mlxrunner/model/architectures/architectures.go` is an edit to an upstream file. A new architecture is upstreamable, so put the whole change on its own branch and merge with `--no-ff` per `docs/protocols/CARRIED-PATCHES.md`:

  ```sh
  git checkout -b carry/mlx-<name> main
  ```

  If the architecture is fork-only, the import line additionally needs `// xollama-hook: <feature-id> — see docs/features/<feature-id>.md` above it plus a Registry row in `docs/protocols/UPSTREAM-SYNC.md` **in the same commit** (the hook-reconciliation check named by that protocol fails a marker with no row).
- **The registration key is the exact first `architectures` entry from the checkpoint's config.json** (`LlamaForCausalLM`, `Qwen3ForCausalLM`, `Qwen3_5MoeForConditionalGeneration`). `model.Register` panics with `model architecture %q already registered` on duplicates; the loader in `mlxrunner/model/model.go` errors `unsupported architecture: <arch>` when no key matches.
- **No MLX calls in the constructor.** `newModel(root *model.Root)` only reads config JSON, validates it, loads the tokenizer, and allocates Go structs. Every MLX array touch happens in `LoadWeights` or `Forward`.
- **Every test that constructs or evaluates an MLX array runs inside the harness in `mlx/mlxtest/mlxtest.go`**, which skips when the MLX dylib is unavailable — that is the case on this Linux dev host. Pure config-parsing tests use plain `testing` and must pass everywhere.

## Instructions

### Step 1 — Pin the architecture string, then open the branch

1. Read the key from the checkpoint:

   ```sh
   grep -m1 -A3 '"architectures"' <model>/config.json
   ```

   Nested HF checkpoints hide it under `text_config` / `model_type` — `parseConfig` in `mlxrunner/model/qwen3_5/qwen3_5.go` and `mlxrunner/model/glimmer/glimmer.go` show the nested-wrapper handling to copy.
2. Confirm nobody registered it:

   ```sh
   grep -rn 'model.Register(' mlxrunner/model/*/*.go
   ```
3. `git checkout -b carry/mlx-<name> main`.

**Verify before Step 2:** `grep -rn '"<ArchString>"' mlxrunner/` prints nothing. If it prints a hit, the work is an alias registration (Step 8b), not a new package.

### Step 2 — Pick the reference implementation and create the package

| Model shape | Copy structure from | Size |
|---|---|---|
| Dense GQA + RoPE + SwiGLU | `mlxrunner/model/llama/llama.go` | 315 lines |
| Same, plus per-head Q/K RMSNorm | `mlxrunner/model/qwen3/qwen3.go` | 334 lines |
| Per-layer attention types / sliding window / vision tower | `mlxrunner/model/glimmer/` | 684 lines + a vision file |
| MoE experts + hybrid linear attention | `mlxrunner/model/qwen3_5/qwen3_5.go` | 1419 lines |
| Mamba / SSM blocks | `mlxrunner/model/nemotron_h/nemotron_h.go` | 1588 lines |
| Aliases onto an existing impl | `mlxrunner/model/qwen3_5_moe/qwen3_5_moe.go` | 14 lines |

Create the new package directory and its main file under `mlxrunner/model/`. Directory name is lowercase, snake_case where needed (`glm4_moe_lite`, `qwen3_5`, `nemotron_h`); the package clause matches the directory exactly. First line is the package doc comment: `// Package <name> provides the <X> text model implementation for MLX.`

Split into extra files in the same package only when the reference does: a config file, a weights file, a blocks file (see `mlxrunner/model/qwen4_exp/`), vision and image-processing files for multimodal, and render/parser files when the model ships its own chat renderer.

**Verify:** the first two lines of the new file show the doc comment and a `package <name>` line that matches the directory.

### Step 3 — Write the Config struct

One struct, three field groups, exactly as in `mlxrunner/model/qwen3/qwen3.go`:

```go
type Config struct {
	HiddenSize            int32   `json:"hidden_size"`
	NumHiddenLayers       int32   `json:"num_hidden_layers"`
	IntermediateSize      int32   `json:"intermediate_size"`
	NumAttentionHeads     int32   `json:"num_attention_heads"`
	NumKeyValueHeads      int32   `json:"num_key_value_heads"`
	VocabSize             int32   `json:"vocab_size"`
	RMSNormEps            float32 `json:"rms_norm_eps"`
	RopeTheta             float32 `json:"rope_theta"`
	HeadDim               int32   `json:"head_dim"`
	MaxPositionEmbeddings int32   `json:"max_position_embeddings"`
	TieWordEmbeddings     bool    `json:"tie_word_embeddings"`

	// Quantization parameters (set during load based on model quantization).
	QuantGroupSize int                               `json:"-"`
	QuantBits      int                               `json:"-"`
	QuantMode      string                            `json:"-"`
	TensorQuant    map[string]*model.TensorQuantInfo `json:"-"`

	// Computed fields.
	Scale     float32 `json:"-"`
	QKNormEps float32 `json:"-"`
}
```

Sizes are `int32` (they feed `mlx.Reshape` directly), epsilons and thetas are `float32`. Derived and quant fields carry `json:"-"`. Embed it in the model as `*Config` so `m.HeadDim` resolves without a field hop.

**Verify:** every JSON tag matches a key in the real checkpoint config:

```sh
python3 -c "import json,sys;print(sorted(json.load(open(sys.argv[1]))))" <model>/config.json
```

and diff against the tags by eye.

### Step 4 — Write the constructor

```go
func init() {
	model.Register("<ArchString>", newModel)
}

func newModel(root *model.Root) (model.Model, error) {
	configData, err := root.Manifest.ReadConfig("config.json")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(configData, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// ... validation, see below ...
}
```

Validate in this order and with these exact messages (see `mlxrunner/model/qwen3/qwen3.go`):

1. `cfg.HiddenSize <= 0` → `invalid hidden_size: %d`
2. `cfg.NumAttentionHeads <= 0` → `invalid num_attention_heads: %d`
3. `cfg.NumKeyValueHeads <= 0` → default to `cfg.NumAttentionHeads` (no error)
4. `cfg.HeadDim == 0` → require `HiddenSize % NumAttentionHeads == 0` (`hidden_size (%d) must be divisible by num_attention_heads (%d)`), then `cfg.HeadDim = cfg.HiddenSize / cfg.NumAttentionHeads`
5. `cfg.HeadDim <= 0` → `invalid head_dim: %d`
6. `NumAttentionHeads % NumKeyValueHeads != 0` → `num_attention_heads (%d) must be divisible by num_key_value_heads (%d)`
7. Zero-value defaults for `RMSNormEps` and `RopeTheta` taken from the model's HF defaults (llama: `1e-5` / `10000`; qwen3: `1e-6` / `1000000`)
8. `cfg.Scale = float32(1.0 / math.Sqrt(float64(cfg.HeadDim)))`

Then quantization and tokenizer, copied verbatim:

```go
	if qt := root.QuantType(); qt != "" {
		cfg.QuantGroupSize, cfg.QuantBits, cfg.QuantMode = model.QuantizationParams(qt)
		if gs := root.GroupSize(); gs > 0 {
			cfg.QuantGroupSize = gs
		}
	} else {
		cfg.QuantGroupSize, cfg.QuantBits, cfg.QuantMode = model.QuantizationParams("")
	}
	cfg.TensorQuant = root.AllTensorQuant()

	tokData, err := root.Manifest.ReadConfig("tokenizer.json")
	if err != nil {
		return nil, fmt.Errorf("load tokenizer config: %w", err)
	}
	tokConfig := &tokenizer.TokenizerConfig{ConfigJSON: configData}
	if genConfigData, err := root.Manifest.ReadConfig("generation_config.json"); err == nil {
		tokConfig.GenerationConfigJSON = genConfigData
	}
	if tokConfigData, err := root.Manifest.ReadConfig("tokenizer_config.json"); err == nil {
		tokConfig.TokenizerConfigJSON = tokConfigData
	}
	tok, err := tokenizer.LoadFromBytesWithConfig(tokData, tokConfig)
	if err != nil {
		return nil, fmt.Errorf("parse tokenizer: %w", err)
	}

	return &Model{Layers: make([]*Layer, cfg.NumHiddenLayers), Config: &cfg, tok: tok}, nil
```

Keep the constructor unexported (`newModel`) unless another package will register aliases onto it — then export it as `NewModel`, the way `qwen3_5.NewModel` is consumed by `mlxrunner/model/qwen3_5_moe/qwen3_5_moe.go`. When config parsing exceeds ~40 lines, factor it into `func parseConfig(data []byte) (Config, error)` so it is unit-testable without a manifest (see `mlxrunner/model/nemotron_h/nemotron_h.go`).

**Verify:**

```sh
go vet ./mlxrunner/model/<name>/
```

is clean of unused-import errors before writing `LoadWeights`.

### Step 5 — Write LoadWeights

Uses the `Config` from Step 4. Structure from `mlxrunner/model/llama/llama.go`:

```go
func resolveWeightPrefix(tensors map[string]*mlx.Array) string {
	for _, prefix := range []string{"", "language_model."} {
		if tensors[prefix+"model.embed_tokens.weight"] != nil {
			return prefix
		}
	}
	return ""
}

func (m *Model) LoadWeights(tensors map[string]*mlx.Array) error {
	m.weightPrefix = resolveWeightPrefix(tensors)
	prefix := m.weightPrefix
	linears := model.NewLinearFactory(tensors, m.QuantGroupSize, m.QuantBits, m.QuantMode, m.TensorQuant)

	embedTokens := model.MakeEmbeddingLayer(tensors, prefix+"model.embed_tokens", m.QuantGroupSize, m.QuantBits, m.QuantMode, m.TensorQuant)
	if embedTokens == nil {
		return fmt.Errorf("missing embedding weight: %smodel.embed_tokens.weight", prefix)
	}
	m.EmbedTokens = embedTokens

	normWeight := tensors[prefix+"model.norm.weight"]
	if normWeight == nil {
		return fmt.Errorf("missing final norm weight: %smodel.norm.weight", prefix)
	}
	m.Norm = nn.NewRMSNorm(normWeight, m.RMSNormEps)

	if m.TieWordEmbeddings {
		m.LMHead = m.EmbedTokens.AsLinear()
	} else if lmHead := linears.Make(prefix + "lm_head"); lmHead != nil {
		m.LMHead = lmHead
	} else if lmHead := linears.Make("lm_head"); lmHead != nil {
		m.LMHead = lmHead
	} else {
		m.LMHead = m.EmbedTokens.AsLinear()
	}

	for i := range m.NumHiddenLayers {
		layerPrefix := fmt.Sprintf("%smodel.layers.%d", prefix, i)
		// norms via nn.NewRMSNorm(tensors[layerPrefix+".input_layernorm.weight"], m.RMSNormEps)
		// projections via linears.Make(layerPrefix + ".self_attn.q_proj") etc.
		// then nil-check every field and return fmt.Errorf("layer %d: missing ...", i)
	}
	return nil
}
```

Rules that are easy to get wrong:

- Build **all** linears through `linears.Make(path)` — never `nn.NewLinear` directly. `Make` picks quantized vs dense from `TensorQuant` and returns `nil` (not an error) when the tensor is absent.
- Pass the *path without* `.weight`; the factory appends `.weight`, `.scales`, `.biases`.
- Nil-check every layer field after the assignments, grouped, with `layer %d: missing attention projections` / `missing mlp projections` / `missing input_layernorm` style messages. A missing tensor must surface here, not as a nil deref in `Forward`.
- Loop with `for i := range m.NumHiddenLayers` (Go 1.22 int32 range), matching the references.

**Verify:** `go build ./mlxrunner/model/<name>/` compiles, and `grep -c 'layer %d: missing'` on the new file is ≥ 3.

### Step 6 — Implement the model.Model interface

Uses the fields from Step 5. All five methods are required by the interface in `mlxrunner/model/model.go`:

```go
func (m *Model) Forward(b *batch.Batch, caches []cache.Cache) (hidden, auxHidden *mlx.Array) {
	dims := b.InputIDs.Dims()
	B, L := int32(dims[0]), int32(dims[1])
	positions := mlx.FromValues(b.SeqOffsets, len(b.SeqOffsets))

	h := m.EmbedTokens.Forward(b.InputIDs)
	for i, layer := range m.Layers {
		var c cache.Cache
		if caches != nil && i < len(caches) {
			c = caches[i]
		}
		h = layer.Forward(h, b, c, positions, B, L, m.Config)
	}
	out := m.Norm.Forward(h, m.RMSNormEps)
	return out, out
}

func (m *Model) Unembed(x *mlx.Array) *mlx.Array    { return m.LMHead.Forward(x) }
func (m *Model) MaxContextLength() int              { return int(m.MaxPositionEmbeddings) }
func (m *Model) Tokenizer() *tokenizer.Tokenizer    { return m.tok }

func (m *Model) NewCaches() []cache.Cache {
	caches := make([]cache.Cache, len(m.Layers))
	for i := range caches {
		caches[i] = cache.NewKVCache()
	}
	return caches
}
```

Cache slot per layer, chosen by layer type:

- full attention → `cache.NewKVCache()`
- sliding-window attention → `cache.NewRotatingKVCache(window)`
- linear/SSM/gated-delta layers → `cache.NewRecurrentCache(convTail, convDim, numVHeads, headVDim, headKDim)`
- a layer that keeps no state → leave the slot `nil` and skip it in `Forward`

`NewCaches()` must return exactly one slot per `m.Layers` entry in layer order; the prefix cache in `mlxrunner/prefix_cache.go` indexes it positionally. A hybrid model's slot layout deserves its own `TestNewCachesLayout` test (see `mlxrunner/model/nemotron_h/nemotron_h_test.go`).

**Verify:** add `var _ model.Model = (*Model)(nil)` below the type and run `go build ./mlxrunner/model/<name>/`.

### Step 7 — Implement the block forwards

```go
func (l *Layer) Forward(x *mlx.Array, b *batch.Batch, c cache.Cache, positions *mlx.Array, B, L int32, cfg *Config) *mlx.Array {
	h := mlx.Add(x, l.Attention.Forward(l.AttentionNorm.Forward(x, cfg.RMSNormEps), b, c, positions, B, L, cfg))
	return mlx.Add(h, l.MLP.Forward(l.MLPNorm.Forward(h, cfg.RMSNormEps)))
}
```

Attention body, in this order (see `mlxrunner/model/qwen3/qwen3.go`):

1. `q/k/v := a.QProj.Forward(x)` …
2. `mlx.Reshape(q, B, L, cfg.NumAttentionHeads, cfg.HeadDim)` — K and V use `cfg.NumKeyValueHeads`
3. per-head Q/K norms (if the model has them) **before** the transpose
4. `mlx.Transpose(q, 0, 2, 1, 3)` on q, k, v
5. `mlx.RoPEWithBase(q, int(cfg.HeadDim), false, cfg.RopeTheta, 1.0, positions)` on q and k — for YaRN/scaled rope use `nn.BuildYarnRopeFreqs` and `nn.ScaleRotaryPart` instead of hand-rolling
6. cache/SDPA:

```go
	var kv nn.SDPAOption
	if c != nil {
		history := c.(cache.Attention).Update(b, k, v)
		kv = nn.WithKVHistory(history)
	} else {
		kv = nn.WithKV(k, v, b.SeqQueryLens)
	}
	out := nn.ScaledDotProductAttention(b, q, cfg.Scale, kv, nn.WithMask(nn.CausalMask()))
	out = mlx.Reshape(mlx.Transpose(out, 0, 2, 1, 3), B, L, cfg.NumAttentionHeads*cfg.HeadDim)
	return a.OProj.Forward(out)
```

Never materialize repeated K/V for GQA — MLX SDPA handles a Q-head multiple of KV heads natively. For sliding-window layers pass `nn.WithMask(nn.SlidingWindowMask(b, K, window, dtype).Intersect(nn.CausalMask()))`.

MLP: `return m.DownProj.Forward(mlx.SwiGLU(m.GateProj.Forward(x), m.UpProj.Forward(x)))`.

**Verify:**

```sh
go build ./mlxrunner/...
golangci-lint run ./mlxrunner/model/<name>/...
```

### Step 8 — Register the package

**8a — new implementation.** Add one blank import to `mlxrunner/model/architectures/architectures.go`, in alphabetical order inside the existing block:

```go
	_ "github.com/ollama/ollama/mlxrunner/model/<name>"
```

That file is the only importer wiring; `mlxrunner/runner.go` already imports the architectures package. If the work is fork-only rather than upstream-bound, prefix the line with the `// xollama-hook:` marker and add the Registry row (see **Critical**).

**8b — alias onto an existing implementation.** Do not copy the model; create a 14-line package in the shape of `mlxrunner/model/qwen3_5_moe/qwen3_5_moe.go`:

```go
func init() {
	model.Register("<Arch>ForConditionalGeneration", qwen3_5.NewModel)
	model.Register("<Arch>ForCausalLM", qwen3_5.NewModel)
}
```

**Verify:** `go build ./mlxrunner/...` then `grep -n '<name>' mlxrunner/model/architectures/architectures.go` shows exactly one line.

### Step 9 — Write the sibling test

Add the test file next to the new package's main file, in `package <name>` (internal — the tests call unexported `parseConfig`, `newModel`, helper funcs).

Minimum coverage:

1. `TestParseConfig` — feed a literal `[]byte` of the real checkpoint config shape and assert derived fields (`Scale`, `HeadDim`, defaults). Plain `testing`, no MLX.
2. `TestParseConfigRejects...` — one case per validation error added in Step 4; assert the error is non-nil.
3. `TestNewCachesLayout` — for hybrid models, assert the concrete type of each slot (`*cache.KVCache`, `*cache.RotatingKVCache`, `*cache.RecurrentCache`) at each index.
4. Any numeric kernel test wrapped in the harness from `mlx/mlxtest/mlxtest.go`:

```go
func TestAttentionMatchesReference(t *testing.T) {
	mlxtest.Run(t, func(t *mlxtest.T) {
		x := mlx.Zeros(mlx.DTypeBFloat16, 1, 4, 8)
		got := ...
		mlx.Eval(got)
		// compare with assertAllClose-style tolerance checks
	})
}
```

Use `t.Fatalf("%s: X = %v, want %v", ...)` messages; no testify in these packages.

**Verify:**

```sh
go test ./mlxrunner/model/<name>/... -run . -v
```

config tests PASS, MLX tests SKIP with `MLX not available` on Linux.

### Step 10 — Full verification, commit, bookkeeping

```sh
go build ./mlxrunner/...
go test ./mlxrunner/...
golangci-lint run
gitleaks protect --staged --config .gitleaks.toml
```

Commit title follows `CONTRIBUTING.md` (`<package>: <short description>`, lowercase, continues "This changes Ollama to…"):

```
mlxrunner/model/<name>: support the <X> architecture
```

Then the OpenWolf bookkeeping required by `.wolf/OPENWOLF.md`: add the new files to `.wolf/anatomy.md`, append a `| HH:MM | … |` row to `.wolf/memory.md`, and log any error you hit along the way to `.wolf/buglog.json`.

**Verify:** `go test ./mlxrunner/...` exits 0 and `git status` shows only the new package, the one line in `mlxrunner/model/architectures/architectures.go`, and the `.wolf/` updates.

## Examples

### Example 1 — new dense architecture

**User says:** "add Mistral3 to mlxrunner, it's a plain llama-style decoder with GQA"

**Actions taken:**
1. `grep -m1 -A3 architectures` on the checkpoint config → `"Mistral3ForCausalLM"`; `grep -rn 'model.Register(' mlxrunner/model/*/*.go` → not present.
2. `git checkout -b carry/mlx-mistral3 main`.
3. A new `mistral3` package created under `mlxrunner/model/` from `mlxrunner/model/llama/llama.go`: `package mistral3`, `init()` registering `"Mistral3ForCausalLM"` → `newModel`, `Config` with `sliding_window int32` added, `Model`/`Layer`/`Attention`/`MLP` structs, `resolveWeightPrefix`, the quant + tokenizer blocks verbatim.
4. `LoadWeights` with `model.NewLinearFactory` / `model.MakeEmbeddingLayer` and per-layer nil checks.
5. `NewCaches` returns `cache.NewRotatingKVCache(int(m.SlidingWindow))` per layer; `Attention.Forward` masks with `nn.SlidingWindowMask(...).Intersect(nn.CausalMask())`.
6. One blank import added to `mlxrunner/model/architectures/architectures.go` between the llama and nemotron_h lines.
7. A sibling test file with `TestParseConfig`, `TestParseConfigRejectsBadHeadDim`, `TestNewCachesLayout`.

**Result:** `go test ./mlxrunner/...` passes (MLX tests skip on Linux); the fork gains `Mistral3ForCausalLM` in one additive package plus a one-line upstream edit, merged from the carry branch with `--no-ff`.

### Example 2 — alias for an existing implementation

**User says:** "the new Qwen3.5 MoE checkpoints report Qwen3NextMoeForCausalLM, make them load"

**Actions taken:** `grep -rn 'Qwen3NextMoe' mlxrunner/` shows `mlxrunner/model/qwen3_5/qwen3_5.go` already implements the shape, so no new model code — the alias goes in `mlxrunner/model/qwen3_5_moe/qwen3_5_moe.go` as another `model.Register("Qwen3NextMoeForCausalLM", qwen3_5.NewModel)` line, and `qwen3_5.NewModel` stays exported.

**Result:** 1 line changed, no new package, no edit to `mlxrunner/model/architectures/architectures.go` (that package is already imported).

## Common Issues

**`panic: model architecture "X" already registered`** — two `init()` functions claim the same key. 1. `grep -rn '"X"' mlxrunner/model/*/*.go` 2. Delete the duplicate `model.Register`; if both packages are wanted, keep one implementation and register the second name as an alias (Step 8b).

**`unsupported architecture: X` at load time** — the loader in `mlxrunner/model/model.go` found no entry. 1. `grep -n '<name>' mlxrunner/model/architectures/architectures.go` — a missing blank import means `init()` never runs. 2. Compare the registered string byte-for-byte with the first `architectures` entry in the checkpoint config; the match is case-sensitive.

**`missing embedding weight: model.embed_tokens.weight`** — the checkpoint uses a prefix `resolveWeightPrefix` does not know. 1. Dump the tensor names: add a temporary `for k := range tensors { fmt.Println(k) }` at the top of `LoadWeights`. 2. Add the observed prefix (e.g. `"model.language_model."`) to the slice in `resolveWeightPrefix`.

**`layer 0: missing attention projections`** — `linears.Make` returned nil. 1. Confirm the path you passed has no `.weight` suffix (the factory appends it). 2. Confirm the checkpoint's naming (`qkv_proj` fused instead of `q_proj`/`k_proj`/`v_proj`) and split the fused tensor in `LoadWeights` as `mlxrunner/model/qwen3_5/gdn_projections.go` does. 3. For quantized checkpoints, verify the sibling `.scales` tensor exists — without it the factory cannot build a `QuantizedLinear`.

**Tests report `MLX not available: ...` and skip** — expected on this Linux host; the harness in `mlx/mlxtest/mlxtest.go` skips when the dylib cannot load. Numeric verification must happen on an Apple Silicon machine. Do not report an MLX-dependent test as passing when it skipped.

**Test crashes or hangs instead of skipping** — an MLX call outside the test harness. Move every `mlx.Zeros` / `mlx.Eval` / layer `Forward` call inside the `mlxtest.Run(t, func(t *mlxtest.T) { ... })` closure; MLX work must run on the shared MLX thread.

**`cannot use m (variable of type *Model) as model.Model value: missing method NewCaches`** — one of the five interface methods is absent or has a drifted signature. Compare against the interface in `mlxrunner/model/model.go`; `Forward` returns two arrays `(hidden, auxHidden)` and plain models return the same array twice.

**Shape panic inside SDPA or `mlx.Reshape`** — the reshape argument order is `(x, B, L, heads, headDim)` and K/V must use `cfg.NumKeyValueHeads`, not `cfg.NumAttentionHeads`. Also check the final reshape uses `cfg.NumAttentionHeads*cfg.HeadDim`, not `cfg.HiddenSize` — they differ whenever `head_dim` is set explicitly in the checkpoint config.

**`golangci-lint run` flags an unused field or func** — a struct field kept "for later" is not accepted here. Delete it, or use it in `LoadWeights`.
