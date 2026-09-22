# Model config carrier — `xollama.json`

## The problem

xollama needs some settings to travel *with a model*: which engine it requires,
which speculative driver to use. Upstream has no field for them, and the obvious
place is the wrong one.

`api.FormatParams` resolves every Modelfile `PARAMETER` name against the json
tags of `api.Options` and returns `unknown parameter '%s'` for anything else. So
a fork setting added there would make a model published from xollama **fail to
create on stock ollama**. That is the opposite of what a soft fork should do —
the model should still work, just without the fork's behaviour.

Hijacking the `LICENSE` layer was the fallback idea. It is not used: a license
layer that is not a license is the kind of thing a fork gets criticised for, and
it only carries one unstructured string.

## The carrier

A layer of media type `application/vnd.ollama.image.json`, named `xollama.json`.

Four properties make this work, and all four are upstream's own behaviour:

| | Why it holds |
|---|---|
| **Stock ollama ignores it** | The layer switch in `server/images.go` has no case for this media type at all, so it is read past in silence. |
| **It cannot collide** | Upstream's own json layers are named `config.json` and `<prefix>/config.json`. The NAME is what separates them, which is why the remove-on-override matches on name as well as media type — dropping by media type alone would delete a safetensors model's real config. |
| **Push and pull carry it** | Neither filters by media type; both iterate `mf.Layers` whole. |
| **The reader already exists** | `manifest.ConfigLayer` / `ReadConfig` / `ReadConfigJSON` are generic over the layer name. |

## Schema

`types/xollama` is an additive package — the schema, and nothing about how the
fork uses it.

```json
{
  "version": 1,
  "engine": "opencoti",
  "draft": { "spec_type": "draft-assistant" }
}
```

| Field | Meaning |
|---|---|
| `version` | Required, and **computed rather than declared** — see below. A version newer than the build is an error, not a warning: these fields change how the model is served, so reading a v2 config as a v1 would serve it differently from how its publisher meant, silently. |
| `engine` | `opencoti` or `llamacpp`. Empty means the model does not care and `XOLLAMA_ENGINE` decides, which is the normal case. Note `auto` is **not** valid: a model saying "auto" is a model saying nothing. |
| `draft.spec_type` | Overrides the `--spec-type` otherwise inferred from the drafter's metadata. |

### The version written is the lowest that is true

`Marshal` recomputes `version` from the fields the config actually uses, and
never from what the build knows. `SchemaVersion` is 2; `SchemaVersionBase` is 1;
only `kv.unified` and `kv.residency_mode` force the 2.

Stamping the newest version unconditionally would have made every model this
build touched unreadable to an older xollama, including models using nothing
newer than v1 — because `Validate` treats a future version as an error, which is
the right call for the reason in the table above. So a config states the oldest
version that is true of it, and only a model actually using a v2 field pays the
v2 floor. It is recomputed rather than defaulted, so a config that was v2 and
has had its v2 fields cleared becomes readable by an older build again.

`TestTheVersionWrittenIsTheLowestThatIsTrue` and
`TestTheStoredVersionFollowsTheFieldsUsed` hold the two ends of that.

Unknown **keys** are accepted, so a model mentioning a setting a newer build
added is still servable. `draft_num_predict` is deliberately absent: it is
already an `api.Options` field and already travels in the params layer. Only
settings upstream has no home for belong here.

## Writing one

```dockerfile
FROM ./gemma-4-e4b.gguf
DRAFT ./gemma-4-E4B-it-assistant-Q8_0.gguf

XOLLAMA {"version": 1, "engine": "opencoti"}
```

The directive takes inline JSON or a path to a `.json` file beside the
Modelfile. It is validated while the Modelfile is parsed, not at create time, so
a typo is reported against the line that caused it. `ollama show --modelfile`
round-trips it.

A model creating `FROM` a parent that already carries a config **replaces** it
rather than inheriting — otherwise a child could never shed a stale engine pin.

`create.ApplyModelfileLayers` reads the request's `Xollama` **pointer**, not
`IsZero`, and the difference carries meaning. `nil` is "this request says
nothing about the fork config", and the parent's layer is inherited untouched —
that is every ordinary create. A non-nil config carrying no settings is "this
request says the fork config is nothing", and removes the layer. `tweak model
--clear` has no other way to say it, and neither does a Modelfile meaning to
strip a parent's engine pin rather than add to it.

### Or without a Modelfile

```sh
xollama tweak model qwen3.6:latest
xollama tweak model qwen3.6:latest --dca=on
xollama tweak model qwen3.6:latest --dca
```

`cmd/tweak` is additive, and everything it knows comes from one table
(`cmd/tweak/fields.go`): the wizard, the flags, the review, the consistency pass
and `--help` all derive from it, so a setting is added by adding a row and the
five cannot disagree about what exists. It writes through the ordinary create
path with `From` set to the model itself, so nothing but this layer changes.

It reads the current config from `/api/show` — which is why `api.ShowResponse`
gained an `Xollama` field — rather than from the manifest, so it works against a
remote server.

Two rules, kept apart deliberately:

- A setting the config **cannot act on** is dropped and named: an opencoti-only
  setting under `engine: "llamacpp"`, a `dca.chunk_size` under
  `dca.enabled: false`. There is nothing to decide.
- A combination where **only the operator knows which half they meant** is
  refused: `kv.unified: false` beside `slots.dynamic: true` is a choice between
  two features. Interactively the refusal re-asks exactly the settings it names
  (`fieldsNamedIn`, which matches a setting by reference rather than by word —
  "engine" appears in half these messages as prose). From a script it is an
  error and a non-zero exit.

The measured cache preconditions in `llm/engine_launch.go` (`ringShapeError`)
are applied here too, through `llm.CacheShapeError`, so a ring without a KVarN
base is refused at configuration time rather than at startup, where it reaches
the operator as a model that will not load. `llm.CacheNeedsFlashAttention` adds
the other one: a KVarN cache with `flash_attention: "off"` is refused, because
the engine refuses the pair at init.

`llm.KnownCacheTypes` is likewise a measurement, taken by asking the pinned
artifact's own parser (`--cache-type-k BOGUS` prints its allowed values). It has
**no kvarn7** — the run kvarn2, 3, 4, 5, 6, 8 invites the assumption and the
engine answers "Unsupported cache type".

[docs/xollama/tweak.mdx](../xollama/tweak.mdx) is the operator-facing page.

## What the fields do

**`engine: "llamacpp"`** skips the engine hook entirely, which is the same path
`XOLLAMA_ENGINE=llamacpp` takes, so the argv stays upstream's.

**`engine: "opencoti"`** fails the load if opencoti was not selected, rather than
running on llama.cpp anyway. The pin exists precisely because the model does not
work there, and the failure would otherwise surface as a load crash with no
mention of the engine. The worked example is a gemma-4 E2B/E4B assistant
drafter: it carries `masked_embd_*` tensors that upstream's loader rejects with a
`vector::_M_range_check` — see [gemma4-drafter.md](gemma4-drafter.md).

**`draft.spec_type`** beats inference, and still goes through
`retargetSpecType`, so pinning `draft-assistant` on llama.cpp resolves to
`draft-mtp` — that engine's spelling for the same driver — rather than a value it
would reject.

**`kv.unified`** (v2) says whether the cells are one pool shared across
sequences or a fixed per-slot split. Until v2 this was *derived*: the server
passed `--kv-unified` exactly when it had parked slots or a shared prefix pool
to admit into, and a model could not say otherwise. The derivation answers a
different question from the one an operator sometimes has — a shared pool lets
ONE long conversation use every cell, a fixed split guarantees each slot its
share, and the total cell count is the same either way. `nil` keeps the
derivation. Stating `false` while dynamic slots or session pooling are on is
refused: both are shares of the same cells.

**`kv.residency_mode`** (v2) is the rolling-KV tactic for a cache that does not
fit in VRAM: `auto`, `head` or `window`. The set is the engine's own, read from
its argument parser (`--kv-residency-mode must be auto|head|window`) rather than
from documentation. It is an opencoti extension, so a config that pins
`llamacpp` and also names a mode is refused rather than served without it.

## Off means off

A model with no `xollama.json` produces no layer, leaves `Model.Xollama` nil, and
takes every upstream path unchanged. The accessors on `LlamaServerConfig` are
nil-tolerant because almost every model will have no config at all.
