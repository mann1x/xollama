# Feature — carrying xollama config with a model

> Status: **design verified, not yet implemented.** The registry round-trip
> below was run for real on 2026-09-18.

xollama exposes engine features ollama has no vocabulary for — split KV, KVarN
tiers, DCA, MTP, rolling-KV. Some of that is per-model, not per-process, and it
has to survive `ollama push` / `ollama pull` so a published model carries its
own settings.

## Why not `PARAMETER`

Closed, and earlier than expected. `api/types.go:1333`:

```go
return nil, fmt.Errorf("unknown parameter '%s'", key)
```

That fires in `FormatParams` at **`ollama create`** time — a Modelfile naming a
parameter ollama does not know never becomes a model at all, let alone a push.
So the knobs cannot ride on `PARAMETER`, and inventing one would also break the
model for anyone running stock ollama.

## The carriers

Layered, most-specific wins:

```
request  >  XOLLAMA_* env  >  local sidecar  >  LICENSE block  >  GGUF KV  >  defaults
```

**1. GGUF metadata KV (`xollama.*`) — preferred where we build the model.**
Invisible to stock ollama (unknown KVs are ignored), travels inside the blob,
no registry surface, nothing shows up in `ollama show`. This is the right
carrier for quants we publish ourselves.

**2. A dedicated LICENSE layer — for models whose GGUF we do not own.**
Its own layer, never appended to the real licence, first line a version marker.
Verified below.

**3. Local sidecar (`$OLLAMA_MODELS/xollama/<manifest-digest>.json`)** — third
party models and user overrides, without recreating the model.

## The LICENSE carrier, verified

`LICENSE` accepts multiple directives, and each becomes its **own** layer
(`create/manifest.go:203` via `LicenseStrings`, which takes `[]string`). On
read they are appended in order to `m.License` (`server/images.go:790`), so
xollama can pick out its own block by marker and hide it.

Modelfile:

```
FROM <base>

LICENSE """Apache License 2.0
...the real licence, untouched...
"""

LICENSE """xollama-config/1
{
  "engine": {"prefer": "opencoti"},
  "kv": {"split": true, "kvarn": {"tier": "q6_0"}}
}
"""
```

**Round trip run 2026-09-18** as `mannix/xollama-config-probe:v1`
(create → push → delete locally → pull), and checked against the registry's own
copy, not just the local one:

| step | result |
|---|---|
| `ollama create` | two `application/vnd.ollama.image.license` layers, distinct digests |
| `ollama push` | accepted; the config layer uploaded as its own blob |
| registry manifest (`GET /v2/mannix/xollama-config-probe/manifests/v1`) | **both license layers present, same digests** |
| registry blob fetch | **byte-identical** to what was pushed |
| `ollama rm` + `ollama pull` | both layers return, digests unchanged |

So the registry neither strips nor rewrites extra license layers, and the
version marker survives intact. The scheme rests on that, and it now rests on a
measurement rather than an assumption.

**The cost, in full:** stock `ollama show --license` prints both blocks
concatenated, so a stock user sees the JSON after the real licence. Cosmetic,
and the reason the marker leads with `xollama-config/1` — it reads as
obviously-not-a-licence, and an unknown future version can be skipped rather
than mis-parsed. xollama filters it out of `show --license`.

## Rejected

- **A custom media-type layer.** `create` will not emit one without changing
  ollama, and an unknown media type is the most likely thing for the registry
  to reject. Strictly worse than a license layer that demonstrably survives.
- **A Go-template comment in `TEMPLATE`.** Survives and is invisible in
  rendered output, but many models have no TEMPLATE layer (GGUF-embedded
  template, or a RENDERER), and adding one *overrides* the model's own
  template — changing inference to carry a setting.
- **`SYSTEM` / `MESSAGE`.** Both contaminate the prompt.
