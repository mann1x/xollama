# macOS: the xOllama app and runtime on Apple silicon

Owner, 2026-10-03: build the macOS app and the macOS runtime with Metal on the
Mac mini, universal if possible, with the opencoti engine and media, signed and
notarized with the setup opencoti already has there.

## What is built

- **Universal app and runtime** (`arm64` + `x86_64`), from upstream's
  `scripts/build_darwin.sh` with Go 1.26.8 (`GOTOOLCHAIN`), Xcode 27, the Metal
  toolchain component and Rosetta 2 (the x86_64 half is cross-built and run
  under it).
- **The opencoti engine on Apple silicon**, package `macos-aarch64`: the APE,
  its signed loader, the Metal library and the three media libraries, staged
  from `llm/engine/pin.txt` into `Contents/Resources/engines`. Design and
  limits: `docs/features/engine-opencoti-llamafile.md`, "macOS".
- **An Intel Mac gets stock llama.cpp only.** opencoti publishes no Intel
  files; the consequence of "universal" is that the same app is smaller in
  function there, never broken.

## Build host

Mac mini M6, 24 GB, macOS 27.0.1 (`ssh macmini`), checkout `~/dev/xollama`.
opencoti works on the same Mac: its directories under `~/dev` are not written
to, and `~/dev/signing` holds secrets that are read there and never copied out,
into a repository or into a log. Homebrew is at `/opt/homebrew/bin`, not on the
ssh `PATH`.

```sh
export PATH=/opt/homebrew/bin:$PATH GOTOOLCHAIN=go1.26.8
export APPLE_IDENTITY="Developer ID Application: <name> (<team>)"
security unlock-keychain -p "$(cat ~/dev/signing/keychain.pass)" oc-signing.keychain
XOLLAMA_NOTARIZE=off XOLLAMA_DMG_HEADLESS=1 sh scripts/build_darwin.sh build sign app
```

Notarization uses an App Store Connect API key: `APPLE_NOTARY_KEY`,
`APPLE_NOTARY_KEY_ID`, `APPLE_NOTARY_ISSUER` (`_notarize`).

## Phases

| phase | what | state |
|---|---|---|
| 1 | universal runtime and app build on the Mac, unsigned | done 2026-10-03 |
| 2 | opencoti on Metal through the loader: routing, launch, discovery, staging | done 2026-10-03, measured |
| 3 | Developer ID signing, engine inside the bundle under the hardened runtime | done 2026-10-03, measured |
| 4 | the app's macOS names: login item, CLI link, state directory, stock Ollama not an instance | built 2026-10-03; needs a run at the Mac's screen |
| 5 | notarization, staple, DMG | done 2026-10-03: app (submission `e793062d`) and DMG (`c95ba8de`) accepted and stapled; Gatekeeper says "Notarized Developer ID" for both. Done by hand from the signed build (the DMG headless); a full `build_darwin.sh` run with notarization on has not been made |
| 6 | hosted or scripted release of the macOS assets, updater feed for macOS | open |
| 7 | image, video and edit models, vision, MTP on Metal | open: untested by opencoti and by us |

## Measured (2026-10-03, opencoti 2610031615001)

512 tokens, one cold and three warm runs, engine inside the signed app; stock
llama.cpp on Metal in brackets: qwen2.5:1.5b 124.5 tok/s (130.3), llama3 31.3
(31.4), gemma3:4b-it-qat 42.2 (42.4). Speech: Kokoro, Supertonic, KittenTTS,
OuteTTS and a custom voice answer and are transcribed back by Whisper on the
same Mac. The Intel half under Rosetta serves on llama.cpp (CPU).

## Open

- The app waits at the system authorization dialog for its CLI link before it
  starts its server; over ssh nobody answers it. Upstream's behaviour, to be
  looked at with phase 4.
- `/usr/local/bin/ollama` on the Mac mini was written by the first, unfixed
  build and points into `xOllama.app`; it needs root to remove.
- The DMG's Finder styling needs a logged-in desktop (`XOLLAMA_DMG_HEADLESS=1`
  skips it).
