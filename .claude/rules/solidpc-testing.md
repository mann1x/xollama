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
  `/api/tags` re-parses all ~123 GGUF headers. Measured: **9.89 s vs 0.06 s** —
  a 165x stall clients report as a timeout or a bogus 404, never as an error.
- The store is
  `/srv/dev-disk-by-uuid-92295e2c-12bd-4d15-a50c-1d80e1a33ee8/spool/ollama_models`.
  Healthy state: `metadata/` is `ollama:ollama`, dir `0755`, files `0644`.
  Check before and after a run:

  ```sh
  find "$OLLAMA_MODELS/metadata" ! -user ollama -o ! -perm -644 | head
  ```

  A model with no cache entry yet is what triggers it — a run that only touches
  already-cached models leaves no trace and proves nothing.
- Invocation that is safe. `HOME` matters: xollama gives a release cut a
  private `HOME` from `DefaultPayloadRoots` in `llm/engine/payload.go` —
  `<lib/ollama>/engines/payload` if writable, else `$HOME/.ollama/engines/payload`
  — where it extracts to `.llamafile/v/<version>/`, so as
  another user it must point somewhere writable, and results must go to a
  directory that user owns
  (`/srv/ml/xollama-phase2/as-ollama`, created `ollama:ollama` `2775`):

  ```sh
  sudo -u ollama env HOME=<writable> XOLLAMA_ENGINE_ARGS=... \
      python3 scripts/phase2-engine-ab.py --engine opencoti --axis overflow \
      --models "$OLLAMA_MODELS" --out /srv/ml/xollama-phase2/as-ollama
  ```
- A dev snapshot extracts nothing: it side-loads the `ggml-cuda.so` beside the
  binary. `sideloaded_dso_sha` hashes that one first and records `source`
  (`beside-artifact` / `llamafile-cache`) — a stale cache hash is a lie.
- **A bare artifact with no `ggml-cuda.so` beside it falls through to the shared
  app dir** `~/.llamafile/v/opencoti-0.10.5-c7/`, which dev builds overwrote 97
  times between 2026-09-05 and 2026-09-22. The executable's own directory always
  wins, so stage the `dso` beside the `bin` — as `cmake/opencoti-fetch.cmake`
  does for a packaged install. A *fat release* boot is safe either way: it byte-
  compares its embedded payload on every start and re-extracts, so it maps its
  own bytes whatever the app dir holds (opencoti #200; the compare→`dlopen`
  window is a residual TOCTOU). From opencoti 0341 the binary names the library
  it mapped (`cuda: loaded … (executable directory, … bytes)`) — read that line
  instead of inferring provenance; `source` only records it from 2026-09-21, so
  older cells cannot self-certify. See `docs/evaluations/phase2-engine-ab.md`.
- **Run a pin candidate on every axis, and on `/api/chat` above all.** Candidate
  `2609220756001` (build 20) was 8/8 on compat and inside build 19's spread on
  throughput and multi-slot, yet refused the *second* `/api/chat` turn of every
  conversation (`kv-reservation: REFUSED`) — compat sends one request per model
  and throughput uses `/api/generate`, so both stayed green on bytes that cannot
  hold a two-turn conversation. Separate our feature from the engine's
  regression by removing the input: `XOLLAMA_SESSION_AFFINITY=false` made every
  failing cell pass, so the pin did not move to it. Build 21
  (`2609221142001`) fixes it — opencoti patch 0345 makes an *unstated* window
  per-request instead of booking the per-session maximum whole — and measures
  8/8 compat, inside build 19's spread, zero `REFUSED` lines across all five
  axes, so **the pin moved there** once the bytes were published.
- **A probe both arms pass is not a discriminator.** The `kvleak4` probe (four
  concurrent `/api/chat`) was meant to reproduce opencoti's distinct-session
  arm, and build 20 passes it too: xollama minted **one** session id for all
  four requests and the bookings read `base need 0`. Read a probe's own logs
  before citing it as coverage.
- **The 3090 is shared with the live service.** A 70B arm holds ~24 GB for the
  run, so the systemd `ollama` cannot load anything meanwhile. Check
  `nvidia-smi --query-compute-apps` first, keep big arms short, and confirm VRAM
  came back — the harness kills the process group and sleeps 5 s between arms.
- **A provisional number is not a measurement.** Re-taken as `ollama` with the
  `bufferSizeRegex` fix, the multislot deficit is −36.2% on the c7 r2 pin and
  gone on builds 18 and 19. Append a re-take to `docs/evaluations/phase2-engine-ab.md`.
- **Match the method before reading a delta.** `--iters` defaults to 3; the
  recorded single-stream figures use 5. For a ~1-point gap, re-run the baseline
  build in the same session instead of comparing against yesterday's figure.
- A repro must be able to reach the defect: `--cli` does not apply the
  server-side reasoning controls, so test thinking-off fixes through `--server`.
- Repairing the cache if it does get broken:

  ```sh
  chown -R ollama:ollama "$OLLAMA_MODELS/metadata"
  chmod 0755 "$OLLAMA_MODELS/metadata"; chmod 0644 "$OLLAMA_MODELS"/metadata/*.json
  ```
