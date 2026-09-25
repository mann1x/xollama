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
#   XOLLAMA_OPENCOTI_ENGINE_ARCH  override the artifact arch label
#
# Iterating on Go code with no network, or without paying for the artifact:
#   cmake -B build . -DXOLLAMA_OPENCOTI_ENGINE=OFF
# The server still runs; it logs the fallback reason and uses llama-server.

option(XOLLAMA_OPENCOTI_ENGINE
    "Bundle the pinned opencoti-llamafile engine into the runtime payload" ON)

set(XOLLAMA_OPENCOTI_ENGINE_FILE "" CACHE FILEPATH
    "Local opencoti-llamafile artifact to stage instead of downloading (verified against llm/engine/pin.txt)")
set(XOLLAMA_OPENCOTI_ENGINE_CACHE "${CMAKE_BINARY_DIR}/opencoti-engine" CACHE PATH
    "Where the fetched engine artifact is cached between builds")
set(XOLLAMA_OPENCOTI_ENGINE_ARCH "" CACHE STRING
    "Artifact arch label to stage; empty means derive it from the target platform")

set(XOLLAMA_ENGINE_PIN "${CMAKE_CURRENT_SOURCE_DIR}/llm/engine/pin.txt")

# Derive the arch label. This mirrors PackageArch in llm/engine/pin.go, and the
# two are kept honest by llm/engine/pin_test.go, which asserts that every
# platform in the routing matrix has an artifact row.
#
# Windows takes the -gpu variant on purpose: the bare artifact is a tenth of
# the size but carries no GPU payload, and an installer that needs a second
# download before it can use the GPU is not an installer.
#
# APPLE is absent by design, not by omission. macOS keeps ollama's MLX path and
# never routes to this engine, so nothing is staged there.
function(_xollama_engine_arch out)
    if(XOLLAMA_OPENCOTI_ENGINE_ARCH)
        set(${out} "${XOLLAMA_OPENCOTI_ENGINE_ARCH}" PARENT_SCOPE)
        return()
    endif()
    if(APPLE)
        set(${out} "" PARENT_SCOPE)
    elseif(WIN32)
        if(CMAKE_SYSTEM_PROCESSOR MATCHES "^(AMD64|amd64|x86_64)$")
            set(${out} "win-x86_64-gpu" PARENT_SCOPE)
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

# Read the artifact filename out of the pin so the staged file keeps its
# published name. That name records which cut is installed, and
# llm/engine/opencoti.go finds it by the artifact prefix, not an exact name.
file(STRINGS "${XOLLAMA_ENGINE_PIN}" _pin_lines REGEX "^[ \t]*bin[ \t]+${_engine_arch}[ \t]")
list(LENGTH _pin_lines _pin_matches)
if(_pin_matches EQUAL 0)
    # Not an error either: a snapshot states what it carries, and one without
    # this platform's row simply has no engine for it -- llm/engine/policy.go
    # (pinUncovered) routes that platform to llama.cpp. Failing here made every
    # Windows configure fail while the pin had no Windows artifact.
    message(STATUS
        "opencoti-llamafile: ${XOLLAMA_ENGINE_PIN} has no 'bin ${_engine_arch}' row; "
        "packages will carry llama.cpp only")
    return()
endif()
if(NOT _pin_matches EQUAL 1)
    message(FATAL_ERROR
        "opencoti-llamafile: expected one 'bin ${_engine_arch}' row in "
        "${XOLLAMA_ENGINE_PIN}, found ${_pin_matches}")
endif()
list(GET _pin_lines 0 _pin_row)
separate_arguments(_pin_fields UNIX_COMMAND "${_pin_row}")
list(GET _pin_fields 2 _engine_rel)
get_filename_component(_engine_name "${_engine_rel}" NAME)

set(_engine_dest "${OLLAMA_PAYLOAD_INSTALL_PREFIX}/${OLLAMA_LIB_DIR}/${_engine_name}")

# Fetching at build time rather than configure time keeps `cmake -B build .`
# fast and makes the artifact a real build dependency: change the pin, and the
# next build restages it.
add_custom_command(
    OUTPUT "${_engine_dest}"
    COMMAND ${CMAKE_COMMAND}
        "-DPIN_FILE=${XOLLAMA_ENGINE_PIN}"
        "-DARCH=${_engine_arch}"
        "-DDEST_DIR=${OLLAMA_PAYLOAD_INSTALL_PREFIX}/${OLLAMA_LIB_DIR}"
        "-DLOCAL_FILE=${XOLLAMA_OPENCOTI_ENGINE_FILE}"
        "-DCACHE_DIR=${XOLLAMA_OPENCOTI_ENGINE_CACHE}"
        -P "${CMAKE_CURRENT_SOURCE_DIR}/cmake/opencoti-fetch.cmake"
    DEPENDS
        "${XOLLAMA_ENGINE_PIN}"
        "${CMAKE_CURRENT_SOURCE_DIR}/cmake/opencoti-fetch.cmake"
    COMMENT "Staging opencoti-llamafile (${_engine_arch})"
    VERBATIM)

add_custom_target(ollama-opencoti-engine ALL
    DEPENDS "${_engine_dest}")

message(STATUS "opencoti-llamafile: staging ${_engine_name} into ${OLLAMA_LIB_DIR}")
