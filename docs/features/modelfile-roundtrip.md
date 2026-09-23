# What `show --modelfile` states, and what it invents

`xollama show --modelfile` is not a debug dump. It is the documented way to
derive a new Modelfile from an existing model — the output says so in its own
header — so anything it invents is fed back to `create` and becomes a real
layer, and anything it drops is lost on the rebuild. Both happened.

## The stray `TEMPLATE`

`GetModel` seeds every model with `template.DefaultTemplate` so the serving path
always has something to render with. `Model.String()` then emitted a `TEMPLATE`
command whenever `m.Template != nil` — which is *always*. A model carrying no
template layer at all printed:

```
TEMPLATE {{ .Prompt }}
```

Feed that back to `create` and the model now has a real 13-byte template layer
it never had, permanently and invisibly changing how it is served. This is not
hypothetical: it is how a published vision tag in this owner's own registry
ended up with a `{{ .Prompt }}` layer its text sibling did not have, and the
asymmetry is what proved the template had been *manufactured* by a rebuild
rather than inherited.

The gate is `m.HasGoTemplate`, not `m.Template != nil`. `HasGoTemplate` is set
in exactly one place — when an `image.prompt` or `image.template` layer is
actually read — so it means precisely "the model defines one".

Verified against a model with `['model', 'params']` layers and nothing else:

| | before | after |
|---|---|---|
| model with no template layer | `TEMPLATE {{ .Prompt }}` | *(nothing)* |
| model with a template layer | states it | states it |

## The dropped `XOLLAMA`

The config layer was the one part of a model that `show --modelfile` silently
discarded. A Modelfile derived from a model pinned to `opencoti` with `kvarn2`
and DCA rebuilt a model with none of that, and nothing in the output said so —
the exact failure the layer exists to prevent, arriving through the tool people
use to copy a model.

`Model.String()` now appends an `XOLLAMA` command, as compact JSON on one line
so it round-trips through the parser's inline-JSON form. It goes through
`Config.Marshal`, not `json.Marshal`, so the printed version is the one a
rebuild would store: `Marshal` recomputes the schema version from the fields
actually used, and printing a stored `v2` for a config whose v2 fields are gone
would make the rebuild look like a version bump that never happened.

A model with no config, or a present-but-empty one, states nothing. An empty
`XOLLAMA` line would read back as *remove the parent's config*, which is the
opposite of saying nothing.

## `XOLLAMA` was rejected by every `create`

Separate defect, same family. `configFromModelfile` in `cmd/create_safetensors.go`
runs on **every** create, only to decide whether the Modelfile is a safetensors
import, and its default branch hands any unrecognised command to
`api.FormatParams`. So a documented directive failed with:

```
Error: unknown parameter 'xollama'
```

before the GGUF path — which handles it correctly — was ever reached. The
command is now recognised there and its argument carried verbatim; parsing it
would mean resolving a relative `.json` path this function has no base
directory for, and the GGUF path validates it anyway.

On a genuine safetensors import it is **refused by name**, not dropped:
`create.PipelineOptions` carries no config layer, so an `XOLLAMA` line on that
path would vanish silently. The error names `xollama tweak model` as the way
round.

## What is still not a round-trip

`create` derives a Go template from a GGUF's own `tokenizer.chat_template`
(`detectChatTemplate` in `server/model.go`). That runs on the import path
regardless of what the Modelfile says, so rebuilding a template-less model from
its GGUF still produces one:

```
mftest:notmpl      ['model', 'params']
mftest:ref-notmpl  ['model', 'params', 'template']     <- rebuilt
```

That is upstream's import behaviour and it is what makes a bare GGUF chattable,
so it is deliberately left alone here. It is a different question from the one
this document answers: `show` no longer *states* a template the model does not
have. A model carrying a config layer round-trips exactly:

```
mftest:withcfg      ['json/xollama.json', 'model', 'params', 'template']
mftest:ref-withcfg  ['json/xollama.json', 'model', 'params', 'template']
```

## Coverage

`server/modelfile_roundtrip_test.go` holds both halves and both negatives;
`TestXollamaIsNotMistakenForAParameter` in `cmd/create_safetensors_test.go`
holds the create-path defect. Registry rows `modelfile-roundtrip` and
`model-config` in [UPSTREAM-SYNC.md](../protocols/UPSTREAM-SYNC.md).
