# Who owns the files xollama writes

## The failure this prevents

Ollama on Linux is a system service. The package installs it, systemd runs it as
the unprivileged `ollama` account, and the model store belongs to that account.
An administrator then runs something as root — a pull, a one-off `serve`, a
benchmark — and every file that command creates is owned by root. The service
cannot read or replace them afterwards.

Nothing about that looks like a permission error, which is why it is dangerous.
Measured on solidPC when it last happened: a model list took **9.89 s instead of
0.06 s**, because the parsed-GGUF metadata cache held 81 root-owned `0600`
entries the service could not open, so it re-parsed every GGUF header on every
request. Clients reported it as a timeout, or as a 404 for a model that was
plainly there.

xollama is a fork people try **alongside an ollama they already depend on**. It
must not be able to damage that installation, so it does not create files as
root.

## The rule

A new file belongs to whoever owns the directory tree it is going into.

`internal/fsowner` walks up from the destination to the nearest directory that
already exists and reads its owner. Two refinements, both learned the hard way:

- **A root-owned ancestor is not evidence.** `/usr/local/lib/ollama` is
  root-owned on every packaged install, and so is a store a previous root
  command already spoiled. Believing either would make the problem permanent.
  In that case xollama falls back to the named service account.
- **A non-root process gets no answer.** It already creates files as itself, and
  `chown(2)` would fail anyway. That is the ordinary path, and it costs one
  `geteuid`.

Directories additionally get setgid and group write. That is not decoration: a
purge removes files by writing the **directory**, not the files, so a
service-group directory stays usable even when it holds entries an earlier root
invocation created.

## What it covers

Every call that creates a file under the shared installation goes through
`internal/fsowner` rather than `os` — `manifest/{paths,manifest,layer}.go`,
`server/{images,create,download,gguf_metadata,model_recommendations,inference_request_log}.go`,
`internal/onboarding/state.go`, `x/transfer/download.go`, and the engine payload
directory in `llm/engine/payload.go`.

Measured end to end rather than argued. A `pull` run **as root** into a store
owned by `ollama`:

| | files not owned by `ollama` | service can write `blobs/`? |
|---|---|---|
| with adoption | **0** | yes |
| without it | 11 | **no** |

The metadata cache from the original incident lands as `ollama:ollama` and is
readable by the service.

## The case a chown cannot fix

The mirror image is an ordinary user running xollama against a store owned by a
system service. `chown(2)` is privileged, so nothing can be handed over, and
every file that process creates will be one the service cannot replace.

There is no safe automatic answer, so `fsowner.Preflight` says it out loud once
at startup, naming the owner, the account in use, the consequence and the fix.
It never blocks startup — someone may be pointing xollama at that store
deliberately, and a warning they can act on beats a refusal they have to work
around.

## Windows

A no-op, by build tag. Ollama installs per user under `%LOCALAPPDATA%`, so there
is no unprivileged service account whose files an elevated process could make
unusable.
