---
paths:
  - fs/gguf/xollama_ternary.go
  - fs/gguf/xollama_ternary_test.go
  - fs/gguf/tensor.go
  - fs/gguf/file_type.go
---

# Ternary GGUF types (`ternary-types` hook)

- `fs/gguf/xollama_ternary.go` is additive. It sizes and names ggml tensor
  types 142 (`tensorTypePQ2_0`) and 143 (`tensorTypePTQ1_0`), the group-128
  ternary types of PrismML's Bonsai 2 that opencoti runs, and the file types
  `fileTypePQ2_0` (141), `fileTypePQ2_0Legacy` (142, the pre-rename value
  still in published GGUFs) and `fileTypePTQ1_0` (143).
- **Why.** Upstream's tables have no size for them, so a tensor's size read as
  0 and `create` refused the file with `unsupported tensor "output.weight"
  size overflows`.
- The hook is four marked `default:` branches, nothing else:
  `typeSize`, `blockSize` and `String` in `fs/gguf/tensor.go` return
  `ternaryTypeSize(tt)`, `ternaryBlockSize(tt)` and `ternaryTensorName(tt)`;
  `String` in `fs/gguf/file_type.go` returns `ternaryFileTypeName(t)`. Never
  add a `case` to upstream's switches: a new type goes in the additive file.
- **Off means off.** Each helper returns upstream's own answer (0, 256,
  `unknown`) for every other type, so a file upstream refuses is still
  refused.
- A group is `ternaryGroup` (128) weights sharing one f16 scale: 2 bits a
  weight for PQ2_0, five trits to the byte for PTQ1_0.
- Guard: `fs/gguf/xollama_ternary_test.go`, sizes held to the published files'
  offsets. Registry row `ternary-types` in `docs/protocols/UPSTREAM-SYNC.md`.

```sh
go test ./fs/gguf/...
```
