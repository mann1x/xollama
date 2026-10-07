#!/bin/sh

# Note:
#  While testing, if you double-click on the xOllama.app
#  some state is left on MacOS and subsequent attempts
#  to build again will fail with:
#
#    hdiutil: create failed - Operation not permitted
#
#  To work around, specify another volume name with:
#
#    VOL_NAME="$(date)" ./scripts/build_darwin.sh
#
VOL_NAME=${VOL_NAME:-"Ollama"}
export VERSION=${VERSION:-$(git describe --tags --first-parent --abbrev=7 --long --dirty --always | sed -e "s/^v//g")}
export CGO_CFLAGS="-O3 -mmacosx-version-min=14.0"
export CGO_CXXFLAGS="-O3 -mmacosx-version-min=14.0"
export CGO_LDFLAGS="-mmacosx-version-min=14.0"

set -e

status() { echo >&2 ">>> $@"; }
usage() {
    echo "usage: $(basename $0) [build package app sign]"
    exit 1
}

mkdir -p dist

# xollama-hook: macos-engine -- Apple silicon only by default (owner,
# 2026-10-03). The Intel half is 22 x86_64-only llama.cpp libraries, which make
# macOS 27 warn that the app has a component macOS 28 will not open, and the
# opencoti engine and the media libraries are arm64 only anyway.
# `-a "arm64 amd64"` still builds upstream's universal app.
ARCHS="arm64"
while getopts "a:h" OPTION; do
    case $OPTION in
        a) ARCHS=$OPTARG ;;
        h) usage ;;
    esac
done
# xollama-hook: macos-stockless -- macOS ships no stock llama-server (owner,
# 2026-10-05, done with opencoti c10): every GGUF load runs on opencoti through
# Metal (llm/engine/stockless.go), so llama.cpp is not compiled here, and an
# Intel Mac, which opencoti does not serve, would have no engine at all.
case " $ARCHS " in *" amd64 "*) echo "build_darwin.sh: Intel (amd64) is not built: macOS has no stock llama-server, and opencoti serves Apple silicon only" >&2; exit 1 ;; esac

shift $(( $OPTIND - 1 ))

_build_darwin() {
    BUILD_CPUS=$(getconf _NPROCESSORS_ONLN)
    BUILD_JOBS=${OLLAMA_BUILD_PARALLEL:-$BUILD_CPUS}
    BUILD_LOAD=${OLLAMA_BUILD_LOAD:-$BUILD_CPUS}
    status "Build parallelism: $BUILD_JOBS, load limit: $BUILD_LOAD"

    SOURCE_BUILD=build/darwin-sources
    status "Preparing shared native sources"
    # xollama-hook: macos-mlx-pin -- MLX is taken as a pinned artifact
    # (llama/runtime-pin-darwin.txt), so its sources are not fetched and it is
    # not compiled. XOLLAMA_MLX=compile builds it as upstream does.
    if _mlx_pinned; then
        cmake -S . -B "$SOURCE_BUILD" -DOLLAMA_MLX_BACKENDS= -DOLLAMA_LLAMA_BACKENDS=
        cmake --build "$SOURCE_BUILD" --target ollama-llama-cpp-source
    else
    cmake -S . -B "$SOURCE_BUILD" -DOLLAMA_MLX_BACKENDS=metal_v3 -DOLLAMA_LLAMA_BACKENDS=
    cmake --build "$SOURCE_BUILD" --target ollama-llama-cpp-source --target ollama-mlx-sources
    fi
    LLAMA_CPP_SHARED_SRC="$(pwd)/$SOURCE_BUILD/_deps/llama_cpp-src"
    MLX_SHARED_SRC="$(pwd)/$SOURCE_BUILD/_deps/mlx-src"
    MLX_C_SHARED_SRC="$(pwd)/$SOURCE_BUILD/_deps/mlx-c-src"

    for ARCH in $ARCHS; do
        status "Building darwin $ARCH"
        INSTALL_PREFIX=dist/darwin-$ARCH/
        BUILD_DIR=build/darwin-$ARCH

        if [ "$ARCH" = "amd64" ]; then
            CMAKE_ARCH=x86_64
            MLX_BACKENDS=metal_v3
            MLX_EXTRA_ARGS="-DMLX_ENABLE_X64_MAC=ON"
            MLX_CGO_CFLAGS="-O3 -mmacosx-version-min=14.0"
            MLX_CGO_LDFLAGS="-ldl -lc++ -framework Accelerate -mmacosx-version-min=14.0"
        else
            CMAKE_ARCH=arm64
            MLX_BACKENDS="metal_v3;metal_v4"
            MLX_EXTRA_ARGS=
            MLX_CGO_CFLAGS="-O3 -mmacosx-version-min=14.0"
            MLX_CGO_LDFLAGS="-lc++ -framework Metal -framework Foundation -framework Accelerate -mmacosx-version-min=14.0"
        fi
        MLX_TARGET="--target ollama-mlx-backends"
        if _mlx_pinned; then MLX_BACKENDS=; MLX_TARGET=; fi # xollama-hook: macos-mlx-pin

        cmake -S . -B "$BUILD_DIR" \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_OSX_ARCHITECTURES=$CMAKE_ARCH \
            -DCMAKE_OSX_DEPLOYMENT_TARGET=14.0 \
            -DCMAKE_INSTALL_PREFIX=$INSTALL_PREFIX \
            -DOLLAMA_PAYLOAD_INSTALL_PREFIX=$INSTALL_PREFIX \
            -DOLLAMA_GO_OUTPUT=$INSTALL_PREFIX/xollama \
            -DOLLAMA_VERSION="$VERSION" \
            -DOLLAMA_MLX_BACKENDS="$MLX_BACKENDS" \
            -DOLLAMA_LLAMA_BACKENDS= \
            -DFETCHCONTENT_SOURCE_DIR_LLAMA_CPP=$LLAMA_CPP_SHARED_SRC \
            -DFETCHCONTENT_SOURCE_DIR_MLX=$MLX_SHARED_SRC \
            -DFETCHCONTENT_SOURCE_DIR_MLX-C=$MLX_C_SHARED_SRC \
            $MLX_EXTRA_ARGS

        GOOS=darwin GOARCH=$ARCH CGO_ENABLED=1 CGO_CFLAGS="$MLX_CGO_CFLAGS" CGO_LDFLAGS="$MLX_CGO_LDFLAGS" \
            cmake --build "$BUILD_DIR" --target ollama-go $MLX_TARGET --parallel "$BUILD_JOBS" -- -l "$BUILD_LOAD" # xollama-hook: macos-stockless (upstream: ollama-local)
    done
    if _mlx_pinned; then _take_mlx_runtime; fi # xollama-hook: macos-mlx-pin
}

# xollama-hook: macos-mlx-pin
# Whether MLX comes from the pin. Apple silicon only: the archive's Metal 4
# library is arm64, and an Intel build is upstream's universal app.
_mlx_pinned() {
    [ "${XOLLAMA_MLX:-pin}" = "pin" ] && [ -f llama/runtime-pin-darwin.txt ] && [ "$ARCHS" = "arm64" ]
}

# xollama-hook: macos-mlx-pin
# Unpacks the pinned MLX runtime where the compiled one would have been
# installed. Refused when the archive is not the pinned bytes, or when it was
# built from another MLX or MLX-C than this tree names.
_take_mlx_runtime() {
    PIN=llama/runtime-pin-darwin.txt
    pin() { awk -v k="$1" '$1==k {print $2; exit}' "$PIN"; }
    [ "$(pin mlx)" = "$(tr -d '[:space:]' < MLX_VERSION)" ] && [ "$(pin mlx_c)" = "$(tr -d '[:space:]' < MLX_C_VERSION)" ] || {
        echo "$PIN was built from MLX $(pin mlx) / MLX-C $(pin mlx_c), but this tree names $(cat MLX_VERSION) / $(cat MLX_C_VERSION): move the pin to the fork release for this base (docs/protocols/FORK-SYNC.md)" >&2
        exit 1
    }
    CACHE=${XOLLAMA_MLX_RUNTIME_CACHE:-build/mlx-runtime}
    ARCHIVE=$CACHE/$(pin sha256)-$(pin asset)
    mkdir -p "$CACHE"
    if [ ! -f "$ARCHIVE" ] || [ "$(shasum -a 256 "$ARCHIVE" | cut -c1-64)" != "$(pin sha256)" ]; then
        status "Fetching the pinned MLX runtime ($(pin repo) $(pin tag))"
        curl -fsSL --retry 5 --retry-all-errors -o "$ARCHIVE.part" "https://github.com/$(pin repo)/releases/download/$(pin tag)/$(pin asset)"
        mv "$ARCHIVE.part" "$ARCHIVE"
    fi
    GOT=$(shasum -a 256 "$ARCHIVE" | cut -c1-64)
    [ "$GOT" = "$(pin sha256)" ] || { echo "$(pin asset) is $GOT, the pin says $(pin sha256)" >&2; exit 1; }
    DEST=dist/darwin-arm64/lib/ollama
    mkdir -p "$DEST"
    rm -rf "$DEST"/mlx_metal_v3 "$DEST"/mlx_metal_v4
    tar -xzf "$ARCHIVE" -C "$DEST"
    # AppleDouble files of upstream's packaging, not part of the runtime.
    find "$DEST" -name '._*' -delete
    for V in mlx_metal_v3 mlx_metal_v4; do
        [ -f "$DEST/$V/libmlx.dylib" ] && [ -f "$DEST/$V/mlx.metallib" ] || { echo "the pinned MLX runtime has no $V" >&2; exit 1; }
    done
    status "MLX runtime: upstream $(pin upstream), from $(pin repo) $(pin tag)"
}

_merge_darwin_payload() {
    status "Preparing universal Darwin runtime payload"
    rm -rf dist/darwin/lib
    mkdir -p dist/darwin/lib/ollama

    for ARCH in $ARCHS; do
        ROOT=dist/darwin-$ARCH/lib/ollama
        [ -d "$ROOT" ] || continue
        for F in "$ROOT"/*; do
            [ -e "$F" ] || continue
            BASE=$(basename "$F")
            case "$BASE" in
                llama-server|llama-quantize|mlx_*) continue ;;
            esac
            [ -e "dist/darwin/lib/ollama/$BASE" ] || cp -P "$F" dist/darwin/lib/ollama/
        done
    done

    for VARIANT in dist/darwin-arm64/lib/ollama/mlx_metal_v*/; do
        [ -d "$VARIANT" ] || continue
        VNAME=$(basename "$VARIANT")
        DEST=dist/darwin/lib/ollama/$VNAME
        AMD_VARIANT=dist/darwin-amd64/lib/ollama/$VNAME
        [ -d "$AMD_VARIANT" ] || AMD_VARIANT=dist/darwin-amd64/lib/ollama
        case "$ARCHS" in *amd64*) ;; *) AMD_VARIANT=/nonexistent ;; esac
        mkdir -p "$DEST"

        for LIB in libmlx.dylib libmlxc.dylib libollama_xgrammar.dylib; do
            if [ -f "$AMD_VARIANT/$LIB" ] && [ -f "$VARIANT$LIB" ]; then
                lipo -create -output "$DEST/$LIB" "$AMD_VARIANT/$LIB" "$VARIANT$LIB"
            elif [ -f "$VARIANT$LIB" ]; then
                cp "$VARIANT$LIB" "$DEST/"
            elif [ -f "$AMD_VARIANT/$LIB" ]; then
                cp "$AMD_VARIANT/$LIB" "$DEST/"
            fi
        done

        for F in "$VARIANT"*; do
            [ -f "$F" ] && [ ! -L "$F" ] || continue
            case "$(basename "$F")" in
                libmlx.dylib|libmlxc.dylib|libollama_xgrammar.dylib) continue ;;
            esac
            cp "$F" "$DEST/"
        done
    done

    _stage_opencoti_engine
}

# xollama-hook: macos-engine
# The opencoti engine for Apple silicon: the APE, its loader, the Metal library
# and the media sidecars, every file verified against llm/engine/pin by the
# script every other package stages with. opencoti publishes nothing for Intel,
# so the files are arm64 only and an Intel Mac stays on llama.cpp
# (llm/engine/policy.go). They go in their own directory, which
# llm/engine/opencoti.go (DefaultDirs) searches first.
_stage_opencoti_engine() {
    [ "${XOLLAMA_OPENCOTI_ENGINE:-ON}" = "OFF" ] && return 0
    case "$ARCHS" in *arm64*) ;; *) return 0 ;; esac
    status "Staging the opencoti engine (macos-aarch64)"
    cmake -DPIN_DIR="$PWD/llm/engine/pin" -DARCH=macos-aarch64 \
        -DDEST_DIR="$PWD/dist/darwin/lib/ollama/engines" \
        -DCACHE_DIR="${XOLLAMA_OPENCOTI_ENGINE_CACHE:-$PWD/build/opencoti-cache}" \
        -DLOCAL_SIDECAR_DIR="${XOLLAMA_OPENCOTI_SIDECAR_DIR:-}" \
        -P cmake/opencoti-fetch.cmake
}

# xollama-hook: macos-engine
# Signs the engine's Mach-O files in a directory. The loader maps the engine's
# code itself, so under the hardened runtime it needs the entitlement in
# app/darwin/engine-loader.entitlements or it is killed at start; and library
# validation makes it refuse a library signed by another team, so the loader
# and its libraries are signed together. The engine file is not a Mach-O.
_sign_opencoti_engine() {
    ENGINE_DIR=$1
    [ -d "$ENGINE_DIR" ] || return 0
    for F in "$ENGINE_DIR"/*.dylib; do
        [ -f "$F" ] || continue
        codesign -f --timestamp -s "$APPLE_IDENTITY" --options=runtime "$F"
    done
    if [ -f "$ENGINE_DIR/ape-macos-aarch64" ]; then
        codesign -f --timestamp -s "$APPLE_IDENTITY" --options=runtime \
            --entitlements app/darwin/engine-loader.entitlements "$ENGINE_DIR/ape-macos-aarch64"
    fi
}

# xollama-hook: macos-engine
# Notarizes one file and waits. An App Store Connect API key
# (APPLE_NOTARY_KEY, APPLE_NOTARY_KEY_ID, APPLE_NOTARY_ISSUER) is used when
# given; otherwise upstream's Apple ID and app password.
# XOLLAMA_NOTARIZE=off signs without submitting, for a build that is tested
# before Apple has answered; such a build is not stapled.
_notarize() {
    if [ "${XOLLAMA_NOTARIZE:-on}" = "off" ]; then
        status "Not notarizing $1 (XOLLAMA_NOTARIZE=off)"
        return 0
    fi
    if [ -n "$APPLE_NOTARY_KEY" ]; then
        xcrun notarytool submit "$1" --wait --timeout "${APPLE_NOTARY_TIMEOUT:-20m}" \
            --key "$APPLE_NOTARY_KEY" --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER"
    else
        xcrun notarytool submit "$1" --wait --timeout 20m --apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID"
    fi
}

_staple() {
    [ "${XOLLAMA_NOTARIZE:-on}" = "off" ] && return 0
    $(xcrun -f stapler) staple "$1"
}

# xollama-hook: macos-engine
# One binary from the architectures in ARCHS: $1 is the output, $2 the path
# under dist/darwin-<arch>/. With one architecture it is that file.
_join_archs() {
    INPUTS=
    for ARCH in $ARCHS; do
        INPUTS="$INPUTS dist/darwin-$ARCH/$2"
    done
    lipo -create -output "$1" $INPUTS
    chmod +x "$1"
    for ARCH in $ARCHS; do
        case $ARCH in amd64) lipo "$1" -verify_arch x86_64 ;; *) lipo "$1" -verify_arch "$ARCH" ;; esac
    done
}

_prepare_darwin_runtime() {
    status "Creating the runtime for: $ARCHS"
    mkdir -p dist/darwin
    _join_archs dist/darwin/xollama xollama
    # xollama-hook: macos-stockless -- no llama-server or llama-quantize

    _merge_darwin_payload
}

_create_darwin_runtime_tarball() {
    status "Creating universal tarball..."
    rm -f dist/xollama-darwin.tar dist/xollama-darwin.tgz
    tar -cf dist/xollama-darwin.tar --strip-components 2 dist/darwin/xollama # xollama-hook: macos-stockless
    tar -rf dist/xollama-darwin.tar --strip-components 4 dist/darwin/lib/ollama
    gzip -9vc <dist/xollama-darwin.tar >dist/xollama-darwin.tgz
}

_package_darwin_runtime() {
    _prepare_darwin_runtime
    _create_darwin_runtime_tarball
}

_sign_darwin() {
    _prepare_darwin_runtime
    if [ -n "$APPLE_IDENTITY" ]; then
        for F in dist/darwin/xollama dist/darwin/lib/ollama/* dist/darwin/lib/ollama/mlx_metal_v*/*; do # xollama-hook: macos-stockless
            [ -f "$F" ] && [ ! -L "$F" ] || continue
            case "$F" in *_LICENSE|*_NOTICE) continue ;; esac
            codesign -f --timestamp -s "$APPLE_IDENTITY" --identifier com.mann1x.xollama --options=runtime "$F"
        done
        _sign_opencoti_engine dist/darwin/lib/ollama/engines

        # create a temporary zip for notarization
        TEMP=$(mktemp -u).zip
        ditto -c -k --keepParent dist/darwin/xollama "$TEMP"
        _notarize "$TEMP"
        rm -f "$TEMP"
    fi

    _create_darwin_runtime_tarball
}

_build_macapp() {
    if ! command -v npm &> /dev/null; then
        echo "npm is not installed. Please install Node.js and npm first:"
        echo "   Visit: https://nodejs.org/"
        exit 1
    fi

    if ! command -v tsc &> /dev/null; then
        echo "Installing TypeScript compiler..."
        npm install -g typescript
    fi

    echo "Installing required Go tools..."

    cd app/ui/app
    npm install
    npm run build
    cd ../../..

    # Build the xOllama.app bundle
    rm -rf dist/xOllama.app
    cp -a ./app/darwin/xOllama.app dist/xOllama.app

    # update the modified date of the app bundle to now
    touch dist/xOllama.app

    go clean -cache
    APP_INPUTS=
    for ARCH in $ARCHS; do
        GOARCH=$ARCH CGO_ENABLED=1 GOOS=darwin go build -o dist/darwin-app-$ARCH -ldflags="-s -w -X=github.com/ollama/ollama/app/version.Version=${VERSION}" ./app/cmd/app
        APP_INPUTS="$APP_INPUTS dist/darwin-app-$ARCH"
    done
    mkdir -p dist/xOllama.app/Contents/MacOS
    lipo -create -output dist/xOllama.app/Contents/MacOS/xOllama $APP_INPUTS
    rm -f $APP_INPUTS

    # Create a mock Squirrel.framework bundle
    mkdir -p dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Versions/A/Resources/
    cp -a dist/xOllama.app/Contents/MacOS/xOllama dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Versions/A/Squirrel
    ln -s ../Squirrel dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Versions/A/Resources/ShipIt
    cp -a ./app/cmd/squirrel/Info.plist dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Versions/A/Resources/Info.plist
    ln -s A dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Versions/Current
    ln -s Versions/Current/Resources dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Resources
    ln -s Versions/Current/Squirrel dist/xOllama.app/Contents/Frameworks/Squirrel.framework/Squirrel

    # Update the version in the Info.plist
    plutil -replace CFBundleShortVersionString -string "$VERSION" dist/xOllama.app/Contents/Info.plist
    plutil -replace CFBundleVersion -string "$VERSION" dist/xOllama.app/Contents/Info.plist

    # Setup the ollama binaries
    mkdir -p dist/xOllama.app/Contents/Resources
    [ -d dist/darwin/lib/ollama ] || _merge_darwin_payload
    cp -a dist/darwin/xollama dist/xOllama.app/Contents/Resources/xollama
    # xollama-hook: macos-stockless -- no llama-server or llama-quantize in the bundle
    if [ -d dist/darwin/lib/ollama ]; then
        cp -a dist/darwin/lib/ollama/. dist/xOllama.app/Contents/Resources/
    fi
    chmod a+x dist/xOllama.app/Contents/Resources/xollama

    # Sign
    if [ -n "$APPLE_IDENTITY" ]; then
        codesign -f --timestamp -s "$APPLE_IDENTITY" --identifier com.mann1x.xollama --options=runtime dist/xOllama.app/Contents/Resources/xollama
        for lib in dist/xOllama.app/Contents/Resources/*.so dist/xOllama.app/Contents/Resources/*.dylib dist/xOllama.app/Contents/Resources/*.metallib dist/xOllama.app/Contents/Resources/mlx_metal_v*/*.dylib dist/xOllama.app/Contents/Resources/mlx_metal_v*/*.metallib dist/xOllama.app/Contents/Resources/mlx_metal_v*/*.so; do
            [ -f "$lib" ] || continue
            codesign -f --timestamp -s "$APPLE_IDENTITY" --identifier com.mann1x.xollama --options=runtime "$lib"
        done
        _sign_opencoti_engine dist/xOllama.app/Contents/Resources/engines
        codesign -f --timestamp -s "$APPLE_IDENTITY" --identifier com.mann1x.xollama --deep --options=runtime dist/xOllama.app
    fi

    rm -f dist/xOllama-darwin.zip
    ditto -c -k --norsrc --keepParent dist/xOllama.app dist/xOllama-darwin.zip
    (cd dist/xOllama.app/Contents/Resources/; tar -cf - xollama *.so *.dylib *.metallib *_LICENSE *_NOTICE mlx_metal_v*/ engines/ 2>/dev/null) | gzip -9vc > dist/xollama-darwin.tgz

    # Notarize and Staple
    if [ -n "$APPLE_IDENTITY" ]; then
        _notarize dist/xOllama-darwin.zip
        rm -f dist/xOllama-darwin.zip
        _staple dist/xOllama.app
        ditto -c -k --norsrc --keepParent dist/xOllama.app dist/xOllama-darwin.zip

        rm -f dist/xOllama.dmg

        # The Finder styling is AppleScript against a logged-in desktop; a build
        # over ssh has none and times out (XOLLAMA_DMG_HEADLESS=1 skips it).
        (cd dist && ../scripts/create-dmg.sh ${XOLLAMA_DMG_HEADLESS:+--skip-jenkins} \
            --volname "${VOL_NAME}" \
            --volicon ../app/darwin/xOllama.app/Contents/Resources/icon.icns \
            --background ../app/assets/background.png \
            --window-pos 200 120 \
            --window-size 800 400 \
            --icon-size 128 \
            --icon "xOllama.app" 200 190 \
            --hide-extension "xOllama.app" \
            --app-drop-link 600 190 \
            --text-size 12 \
            "xOllama.dmg" \
            "xOllama.app" \
        ; )
        rm -f dist/rw*.dmg

        codesign -f --timestamp -s "$APPLE_IDENTITY" --identifier com.mann1x.xollama --options=runtime dist/xOllama.dmg
        _notarize dist/xOllama.dmg
        _staple dist/xOllama.dmg
    else
        echo "WARNING: Code signing disabled, this bundle will not work for upgrade testing"
    fi
}

if [ "$#" -eq 0 ]; then
    _build_darwin
    _sign_darwin
    _build_macapp
    exit 0
fi

for CMD in "$@"; do
    case $CMD in
        build) _build_darwin ;;
        package) _package_darwin_runtime ;;
        sign) _sign_darwin ;;
        app) _build_macapp ;;
        *) usage ;;
    esac
done
