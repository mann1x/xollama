# Architectures the pinned opencoti does not serve

- Stock llama.cpp gains a model architecture before opencoti does (every sync
  moves `LLAMA_CPP_VERSION`; opencoti ports later). `llm/engine/arch.go`
  (`archFeatures`) lists each such architecture with the pin feature that says
  the engine serves it: `"lfm3": "arch_lfm3_v1"` (llama.cpp #31252, b11963;
  not in c13). `engine.ServesArch` asks the pin (`loadPin`, so `withPin` stubs
  it); an architecture with no row is the engine's to serve or refuse.
- `archEngine` (`server/arch_engine.go`), chained after `decisionEngine` in
  `launchXollama`, names `llamacpp` in the launch config while the pin lacks
  the feature, logged once per model at **Warn** (a fallback is loud). It is
  the same path as a model's own `engine: llamacpp`, so placement, estimate and
  the deny-list all see it through `llamaServerConfigForModel`.
- **An engine the model states is kept**, and **macOS keeps the load on
  opencoti** (`engine.StockShipped`): moving it there would trade the engine's
  "unknown model architecture" for the generic stockless refusal.
- **No change when opencoti ships it.** The engine declares the feature in its
  pin and the model goes back to opencoti. Tell opencoti the feature name
  (`arch_<arch>_v1`) when a row is added. A row with an empty feature records
  an architecture the pinned engine was checked to serve already.
- **Every llama.cpp move is checked**: `scripts/check-engine-archs.sh
  [<base-ref>]` diffs llama.cpp's `src/llama-arch.cpp` between the base's and
  the head's `LLAMA_CPP_VERSION` and fails for an architecture with no row. It
  runs in `.github/workflows/compat-origin.yaml` and is step 6 of the sync
  workflow in `docs/protocols/UPSTREAM-SYNC.md`. Keep rows one per line,
  `"<arch>": "<feature>",` — the script reads the file as text
  (`TestTheSyncCheckCanReadEveryRow`).
- Additive only (`server/arch_engine.go`, `llm/engine/arch.go`, the call in our
  own `server/xollama_settings.go`): no upstream file, no Registry row.
  Guards: `server/arch_engine_test.go` (the launch test fails with the call
  removed), `llm/engine/arch_test.go`. Live check:
  `scripts/gates/arch-stock.sh` (G4), which sets no `XOLLAMA_ENGINE` because
  `scripts/gates/serve-side.sh` forces opencoti. Prose:
  `docs/features/engine-opencoti-llamafile.md`, "Architectures opencoti does
  not have yet".
