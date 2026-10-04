# cmake/opencoti-engine.cmake — stage the pinned opencoti-llamafile engine into
# the runtime payload, so it ships inside the installation package beside
# llama-server.
#
# xollama NEVER downloads an engine at runtime. This is where it is acquired,
# once, at build time. Everything after this is a local file lookup:
# llm/engine/opencoti.go searches ~/.ollama/engines/, <libdir>/engines/ and
# <libdir>/, and falls back to the stock llama-server if it finds nothing.
#
# Packaging is automatic from here. cmake/local.cmake ends with a catch-all
#   install(DIRECTORY "${OLLAMA_PAYLOAD_INSTALL_PREFIX}/${OLLAMA_LIB_DIR}/" ...
#           USE_SOURCE_PERMISSIONS)
# so anything staged into that directory is packaged, with its execute bit.
# That is why this file stages into the payload prefix rather than building a
# separate install(FILES) rule that would need keeping in step.
#
# Options:
#   XOLLAMA_OPENCOTI_ENGINE       ON/OFF (default ON)
#   XOLLAMA_OPENCOTI_ENGINE_FILE  stage this local artifact instead of fetching
#   XOLLAMA_OPENCOTI_ENGINE_CACHE where downloads are kept between builds
#   XOLLAMA_OPENCOTI_ENGINE_ARCH  override the platform label
#   XOLLAMA_OPENCOTI_SIDECAR_DIR  take every file but the engine (its GPU
#                                 libraries, media libraries, licence texts)
#                                 from this directory, by published file name
#
#   cmake -B build . -DXOLLAMA_OPENCOTI_ENGINE=OFF
# The server still runs; it logs the fallback reason and uses llama-server.

option(XOLLAMA_OPENCOTI_ENGINE
    "Bundle the pinned opencoti-llamafile engine into the runtime payload" ON)

set(XOLLAMA_OPENCOTI_ENGINE_FILE "" CACHE FILEPATH
    "Local opencoti-llamafile artifact to stage instead of downloading (verified against llm/engine/pin)")
set(XOLLAMA_OPENCOTI_ENGINE_CACHE "${CMAKE_BINARY_DIR}/opencoti-engine" CACHE PATH
    "Where the fetched engine artifact is cached between builds")
set(XOLLAMA_OPENCOTI_ENGINE_ARCH "" CACHE STRING
    "Artifact arch label to stage; empty means derive it from the target platform")

set(XOLLAMA_OPENCOTI_SIDECAR_DIR "" CACHE PATH
    "Directory holding every pinned file but the engine under its published name, staged instead of downloading (verified against llm/engine/pin)")

set(XOLLAMA_ENGINE_PIN_DIR "${CMAKE_CURRENT_SOURCE_DIR}/llm/engine/pin")

# Derive the platform label: the platform column of a pin's `file` rows. This
# mirrors PackageArch in llm/engine/pin.go, and the two are kept honest by
# llm/engine/pin_test.go, which asserts that every platform in the routing
# matrix is served by the pin or refused for a stated reason.
#
# APPLE stages nothing HERE: scripts/build_darwin.sh configures once per
# architecture and merges the two into a universal payload, so it stages the
# arm64-only engine once, after the merge (_stage_opencoti_engine), with the
# same cmake/opencoti-fetch.cmake.
function(_xollama_engine_arch out)
    if(XOLLAMA_OPENCOTI_ENGINE_ARCH)
        set(${out} "${XOLLAMA_OPENCOTI_ENGINE_ARCH}" PARENT_SCOPE)
        return()
    endif()
    if(APPLE)
        set(${out} "" PARENT_SCOPE)
    elseif(WIN32)
        if(CMAKE_SYSTEM_PROCESSOR MATCHES "^(AMD64|amd64|x86_64)$")
            set(${out} "win-x86_64" PARENT_SCOPE)
        else()
            set(${out} "" PARENT_SCOPE)
        endif()
    elseif(UNIX)
        if(CMAKE_SYSTEM_PROCESSOR MATCHES "^(x86_64|AMD64|amd64)$")
            set(${out} "x86_64" PARENT_SCOPE)
        elseif(CMAKE_SYSTEM_PROCESSOR MATCHES "^(aarch64|arm64)$")
            set(${out} "aarch64" PARENT_SCOPE)
        else()
            set(${out} "" PARENT_SCOPE)
        endif()
    else()
        set(${out} "" PARENT_SCOPE)
    endif()
endfunction()

if(NOT XOLLAMA_OPENCOTI_ENGINE)
    message(STATUS "opencoti-llamafile: disabled (XOLLAMA_OPENCOTI_ENGINE=OFF); packages will carry llama.cpp only")
    return()
endif()

_xollama_engine_arch(_engine_arch)
if(_engine_arch STREQUAL "")
    # Not an error. macOS and untested platforms legitimately ship without the
    # engine, and llm/engine/policy.go already routes them to llama.cpp.
    message(STATUS
        "opencoti-llamafile: no artifact is published for ${CMAKE_SYSTEM_NAME}/${CMAKE_SYSTEM_PROCESSOR}; "
        "packages will carry llama.cpp only")
    return()
endif()

# Read the engine's file name out of its pin: it is staged under its published
# name, which records which build is installed, and llm/engine/opencoti.go
# finds it by its prefix, not an exact name. The engine is one file for every
# platform (`file any bin`); Windows has a row of its own for the same bytes
# under the .exe name it needs. A platform takes its own row when there is one.
# cmake/opencoti-fetch.cmake makes the same choice and does all the checking.
file(STRINGS "${XOLLAMA_ENGINE_PIN_DIR}/index.txt" _index_engine REGEX "^[ \t]*engine[ \t]")
if(NOT _index_engine MATCHES "^[ \t]*engine[ \t]+([^ \t]+)[ \t]+rev[ \t]")
    message(FATAL_ERROR "opencoti-llamafile: ${XOLLAMA_ENGINE_PIN_DIR}/index.txt names no engine pin")
endif()
get_filename_component(_engine_pin "${CMAKE_MATCH_1}" NAME)
set(_engine_pin "${XOLLAMA_ENGINE_PIN_DIR}/${_engine_pin}")
file(STRINGS "${_engine_pin}" _pin_lines REGEX "^[ \t]*file[ \t]+${_engine_arch}[ \t]+bin[ \t]")
if(NOT _pin_lines)
    file(STRINGS "${_engine_pin}" _pin_lines REGEX "^[ \t]*file[ \t]+any[ \t]+bin[ \t]")
endif()
list(LENGTH _pin_lines _pin_matches)
if(_pin_matches EQUAL 0)
    # Not an error: a pin states what it carries, and one without this
    # platform's row has no engine for it -- llm/engine/policy.go
    # (pinUncovered) routes that platform to llama.cpp.
    message(STATUS
        "opencoti-llamafile: ${_engine_pin} has no bin row for ${_engine_arch}; "
        "packages will carry llama.cpp only")
    return()
endif()
if(NOT _pin_matches EQUAL 1)
    message(FATAL_ERROR
        "opencoti-llamafile: expected one bin row for ${_engine_arch} in "
        "${_engine_pin}, found ${_pin_matches}")
endif()
list(GET _pin_lines 0 _pin_row)
separate_arguments(_pin_fields UNIX_COMMAND "${_pin_row}")
list(GET _pin_fields 3 _engine_rel)
get_filename_component(_engine_name "${_engine_rel}" NAME)

# Windows stages into lib/ollama/engines, which llm/engine/opencoti.go
# (DefaultDirs) searches before lib/ollama. llama-server.exe lives in
# lib/ollama, and ggml's loader falls back to ggml-cuda.dll in its own
# directory: a library of the engine's staged there under that name would be
# loaded by the stock runtime. The engine's now carry their platform in the
# name (ggml-cuda-win-x86_64.dll), and the directory stays its own.
set(_engine_dir "${OLLAMA_PAYLOAD_INSTALL_PREFIX}/${OLLAMA_LIB_DIR}")
if(_engine_arch MATCHES "^win-")
    string(APPEND _engine_dir "/engines")
endif()
set(_engine_dest "${_engine_dir}/${_engine_name}")

# Every pin file is an input: a component that moves restages the payload.
file(GLOB _engine_pin_files CONFIGURE_DEPENDS "${XOLLAMA_ENGINE_PIN_DIR}/*.txt")

# Fetching at build time rather than configure time keeps `cmake -B build .`
# fast and makes the artifact a real build dependency: change the pin, and the
# next build restages it.
add_custom_command(
    OUTPUT "${_engine_dest}"
    COMMAND ${CMAKE_COMMAND}
        "-DPIN_DIR=${XOLLAMA_ENGINE_PIN_DIR}"
        "-DARCH=${_engine_arch}"
        "-DDEST_DIR=${_engine_dir}"
        "-DLOCAL_FILE=${XOLLAMA_OPENCOTI_ENGINE_FILE}"
        "-DCACHE_DIR=${XOLLAMA_OPENCOTI_ENGINE_CACHE}"
        "-DLOCAL_SIDECAR_DIR=${XOLLAMA_OPENCOTI_SIDECAR_DIR}"
        -P "${CMAKE_CURRENT_SOURCE_DIR}/cmake/opencoti-fetch.cmake"
    DEPENDS
        ${_engine_pin_files}
        "${CMAKE_CURRENT_SOURCE_DIR}/cmake/opencoti-fetch.cmake"
    COMMENT "Staging opencoti-llamafile (${_engine_arch})"
    VERBATIM)

add_custom_target(ollama-opencoti-engine ALL
    DEPENDS "${_engine_dest}")

message(STATUS "opencoti-llamafile: staging ${_engine_name} into ${OLLAMA_LIB_DIR}")
