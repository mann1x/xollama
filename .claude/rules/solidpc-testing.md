---
paths:
  - scripts/phase2-engine-ab.py
  - docs/evaluations/**
---

# Running xollama against the real model store on solidPC

- **Run the test server as `ollama`, never as root.** ollama 0.34.x caches
  parsed GGUF metadata under `$OLLAMA_MODELS/metadata/`. A command run as root
  creates entries there `root:root` mode `0600`; the systemd `ollama` service
  runs as `User=ollama` and can then neither read nor write them, so every
  `/api/tags` re-parses all ~123 GGUF headers. Measured cost when it happened:
  **9.89 s vs 0.06 s** — a 165x stall that clients report as a timeout or a
  bogus 404, never as an error.
- The store is
  `/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models`.
  Healthy state: `metadata/` is `ollama:ollama`, dir `0755`, files `0644`.
  Check before and after a run:

  ```sh
  find "$OLLAMA_MODELS/metadata" ! -user ollama -o ! -perm -644 | head
  ```

  A model with no cache entry yet is what triggers it — a run that only touches
  already-cached models leaves no trace and proves nothing.
- Invocation that is safe. `HOME` matters: the engine extracts its payload to
  `~/.llamafile/v/<version>/`, so as another user it must point somewhere
  writable, and results must go to a directory that user owns
  (`/srv/ml/xollama-phase2/as-ollama`, created `ollama:ollama` `2775`):

  ```sh
  sudo -u ollama env HOME=<writable> XOLLAMA_ENGINE_ARGS=... \
      python3 scripts/phase2-engine-ab.py --engine opencoti --axis overflow \
      --models "$OLLAMA_MODELS" --out /srv/ml/xollama-phase2/as-ollama
  ```
- **The 3090 is shared with the live service.** A 70B arm holds ~24 GB for the
  length of the run, so the systemd `ollama` cannot load anything while it
  lasts. Check `nvidia-smi --query-compute-apps` first, keep big arms short, and
  confirm VRAM came back afterwards — the harness already kills the whole
  process group and sleeps 5 s between arms for this reason.
- **A provisional number is not a measurement.** Re-taken as `ollama` with the
  `bufferSizeRegex` fix, the multislot deficit is −36.2% on the c7 r2 pin and
  gone on build 18. Append a re-take to `docs/evaluations/phase2-engine-ab.md`.
- Repairing the cache if it does get broken:

  ```sh
  chown -R ollama:ollama "$OLLAMA_MODELS/metadata"
  chmod 0755 "$OLLAMA_MODELS/metadata"; chmod 0644 "$OLLAMA_MODELS"/metadata/*.json
  ```
