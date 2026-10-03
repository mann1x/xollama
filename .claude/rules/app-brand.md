---
paths:
  - app/wintray/brand_xollama.go
  - app/wintray/messages.go
  - app/wintray/tray.go
  - app/cmd/app/webview.go
  - app/ui/ui.go
  - app/ui/app/vite.config.ts
  - app/ui/app/xollama-brand.ts
  - app/ui/app/xollama-brand.test.ts
  - docs/features/rebrand.md
---

# The desktop app's name (`app-brand`)

- The app says **xOllama** wherever Windows or its UI names it. A stock
  Ollama tray can sit beside it with the same icon shape; "Ollama" on both
  leaves the user unable to tell them apart (found on eleven2go, 2026-09-28).
- **One constant for the Go side:** `AppName` in the additive
  `app/wintray/brand_xollama.go`. The hooks read it: the tray tooltip
  (`app/wintray/tray.go`), `firstTimeTitle`, `updateMessage`,
  `quitMenuTitle`, `openAppsMenuTitle` (`app/wintray/messages.go`), and the
  window title (`app/cmd/app/webview.go`, plus its `wintray` import). Never
  write the literal again.
- **The UI's ~170 strings are never edited.** `xollamaBrand()` in
  `app/ui/app/xollama-brand.ts` (one plugin line plus its import in
  `app/ui/app/vite.config.ts`) rewrites `Ollama` → `xOllama` at build time
  (`apply: "build"`), and the `<title>` in `index.html`. Editing the sources
  would conflict on every upstream sync.
- **"Ollama" that names ollama.com stays:** `Ollama account`, `Ollama.com`.
  Identifiers are kept by the word boundary (`useOllama`, `OllamaModel`) and by
  skipping `src/lib/ollama-client.ts`, the one file using ollama-js's `Ollama`
  class, plus `node_modules` and tests.
- `app/ui/app/xollama-brand.test.ts` fails if any other source starts using
  `Ollama` as code — it would be renamed by the build. Use the client module or
  another name.
- Every edited line carries `// xollama-hook: app-brand`; Registry row
  `app-brand` in `docs/protocols/UPSTREAM-SYNC.md`; prose in
  `docs/features/rebrand.md`. The icons in `app/assets/` are xOllama's own
  (binary, no marker): upstream's with a red X painted on, made by
  `scripts/xollama-icon.py` from `git show upstream/main:app/assets/<name>.ico`.
