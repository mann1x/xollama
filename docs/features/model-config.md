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
| `version` | Required. A version **newer than the build** is an error, not a warning — these fields change how the model is served, so reading a v2 config as a v1 would serve it differently from how its publisher meant, silently. |
| `engine` | `opencoti` or `llamacpp`. Empty means the model does not care and `XOLLAMA_ENGINE` decides, which is the normal case. Note `auto` is **not** valid: a model saying "auto" is a model saying nothing. |
| `draft.spec_type` | Overrides the `--spec-type` otherwise inferred from the drafter's metadata. |

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

## Off means off

A model with no `xollama.json` produces no layer, leaves `Model.Xollama` nil, and
takes every upstream path unchanged. The accessors on `LlamaServerConfig` are
nil-tolerant because almost every model will have no config at all.
