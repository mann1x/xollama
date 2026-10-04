# cmake/opencoti-fetch.cmake — stage the pinned opencoti engine and everything
# that travels beside it into the runtime payload. Run as a script
# (`cmake -P`), never included.
#
# This is the whole of xollama's engine acquisition, and it happens exactly
# once, at BUILD time. The runtime never downloads an engine: a model load must
# not be able to stall on a 650 MB fetch, and an installed package must work
# with no network at all. llm/engine/opencoti.go only ever looks for an
# artifact that is already on disk.
#
# The pin is opencoti's pin format 2 (their docs/protocols/PIN_FORMAT.md): our
# own index, llm/engine/pin/index.txt, names the components this tree takes,
# and each component's pin file is vendored beside it, byte-identical to the
# published one. Nothing but payload files is fetched: a pin is never read
# from the network. llm/engine/pin.go reads the same directory with the same
# rules; TestCMakeStagesWhatGoReads holds the two to one answer.
#
# Required:
#   -DPIN_DIR=<path>    llm/engine/pin
#   -DARCH=<platform>   x86_64 | aarch64 | win-x86_64 | macos-aarch64
#   -DDEST_DIR=<path>   the directory the engine is staged in
# Optional:
#   -DLOCAL_FILE=<path> stage this file as the engine instead of downloading
#   -DLOCAL_SIDECAR_DIR=<dir>  take every other file from this directory, by
#                       its published file name; a file missing there is an
#                       error (an offline build must not reach for the network)
#   -DCACHE_DIR=<path>  keep downloads here so a clean rebuild is free
#   -DMANIFEST=<path>   write what was staged, one line per file:
#                       <component> <version> <kind> <staged name> <sha256> <bytes>
#
# Every file is verified, sha256 and size, on every path, including the local
# ones. A mismatch is a hard error: shipping an unverified inference engine
# inside an installer is not a thing to warn about and continue past.
#
# Files are staged under their published names, never renamed: that name is
# what the engine looks for in its own directory. The one exception is not a
# file the engine reads: every component publishes a BUILD_INFO.md, so in one
# directory they are staged as BUILD_INFO.<component>.md.

cmake_minimum_required(VERSION 3.24)

foreach(_required PIN_DIR ARCH DEST_DIR)
    if(NOT DEFINED ${_required} OR "${${_required}}" STREQUAL "")
        message(FATAL_ERROR "opencoti-fetch: -D${_required}=<value> is required")
    endif()
endforeach()

set(_platforms x86_64 aarch64 win-x86_64 macos-aarch64)
if(NOT ARCH IN_LIST _platforms)
    message(FATAL_ERROR "opencoti-fetch: -DARCH=${ARCH} is not one of ${_platforms}")
endif()
if(NOT EXISTS "${PIN_DIR}/index.txt")
    message(FATAL_ERROR "opencoti-fetch: no index at ${PIN_DIR}/index.txt")
endif()

set(_components engine cuda cuda12 sbsa vulkan macos media)
set(_hex64 "^[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]$")
set(_hex40 "^[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]$")

# Reads one pin or index file into _st_keys / _st_rows: the statements, in
# order, comments and blank lines dropped. Whole-line comments go first:
# CMake's regex "." does not match the bytes of a multi-byte UTF-8 character,
# so "#.*$" alone leaves the tail of a comment containing one.
macro(_pin_statements file)
    set(_st_rows "")
    file(STRINGS "${file}" _raw)
    foreach(_line IN LISTS _raw)
        string(STRIP "${_line}" _line)
        if(_line MATCHES "^#" OR _line STREQUAL "")
            continue()
        endif()
        string(REGEX REPLACE "#.*$" "" _line "${_line}")
        string(STRIP "${_line}" _line)
        if(_line STREQUAL "")
            continue()
        endif()
        string(REGEX REPLACE "[ \t]+" "|" _line "${_line}")
        list(APPEND _st_rows "${_line}")
    endforeach()
    list(GET _st_rows 0 _first)
    if(NOT _first STREQUAL "format|2")
        message(FATAL_ERROR "opencoti-fetch: ${file}: the first statement must be `format 2`")
    endif()
endmacro()

# ---- the index: which components this tree takes -----------------------------
set(_channel "")
set(_taken "")
_pin_statements("${PIN_DIR}/index.txt")
foreach(_row IN LISTS _st_rows)
    string(REPLACE "|" ";" _f "${_row}")
    list(GET _f 0 _key)
    list(LENGTH _f _n)
    if(_key STREQUAL "format")
    elseif(_key STREQUAL "channel" AND _n GREATER 1)
        list(GET _f 1 _channel)
    elseif(_key STREQUAL "tag" AND _n GREATER 1)
    elseif(_key IN_LIST _components)
        if(_n EQUAL 2 AND _row STREQUAL "${_key}|absent")
            continue()
        endif()
        if(_n LESS 8)
            message(FATAL_ERROR "opencoti-fetch: index: malformed ${_key} line")
        endif()
        list(GET _f 1 _ix_path)
        list(GET _f 2 _w1)
        list(GET _f 3 _ix_rev)
        list(GET _f 4 _w2)
        list(GET _f 5 _ix_sha)
        list(GET _f 6 _w3)
        list(GET _f 7 _ix_version)
        if(NOT "${_w1}|${_w2}|${_w3}" STREQUAL "rev|sha256|version"
           OR NOT _ix_rev MATCHES "${_hex40}" OR NOT _ix_sha MATCHES "${_hex64}")
            message(FATAL_ERROR "opencoti-fetch: index: malformed ${_key} line")
        endif()
        get_filename_component(_ix_file "${_ix_path}" NAME)
        set(_ix_${_key}_file "${_ix_file}")
        set(_ix_${_key}_sha "${_ix_sha}")
        set(_ix_${_key}_version "${_ix_version}")
        list(APPEND _taken "${_key}")
    else()
        message(FATAL_ERROR "opencoti-fetch: index: unknown KEY '${_key}'")
    endif()
endforeach()
if(NOT "engine" IN_LIST _taken)
    message(FATAL_ERROR "opencoti-fetch: ${PIN_DIR}/index.txt names no engine")
endif()
if(_channel STREQUAL "")
    message(FATAL_ERROR "opencoti-fetch: ${PIN_DIR}/index.txt has no channel")
endif()

# ---- the component pins ------------------------------------------------------
# The engine first: every other component is checked against its abi lines and
# its version.
list(REMOVE_ITEM _taken engine)
list(PREPEND _taken engine)

set(_known_keys format component version built-from repo rev abi abi-source
    engine-min gated-with feature sass requires absent file)
set(_engine_abis "")
set(_engine_version "")
set(_engine_name "")
# The files to stage, as parallel lists.
set(_s_component "")
set(_s_version "")
set(_s_kind "")
set(_s_repo "")
set(_s_rev "")
set(_s_path "")
set(_s_sha "")
set(_s_bytes "")

foreach(_c IN LISTS _taken)
    set(_pin "${PIN_DIR}/${_ix_${_c}_file}")
    if(NOT EXISTS "${_pin}")
        message(FATAL_ERROR "opencoti-fetch: the index names ${_ix_${_c}_file} and ${PIN_DIR} does not hold it")
    endif()
    # The vendored copy is the published one, or it is not a pin.
    file(SHA256 "${_pin}" _pin_sha)
    if(NOT _pin_sha STREQUAL "${_ix_${_c}_sha}")
        message(FATAL_ERROR
            "opencoti-fetch: ${_pin} is not the pin the index names.\n"
            "  index    ${_ix_${_c}_sha}\n"
            "  vendored ${_pin_sha}")
    endif()

    set(_p_component "")
    set(_p_version "")
    set(_p_repo "")
    set(_p_rev "")
    set(_p_engine_min "")
    set(_p_abis "")
    set(_p_plat "")
    set(_p_kind "")
    set(_p_path "")
    set(_p_sha "")
    set(_p_bytes "")
    _pin_statements("${_pin}")
    foreach(_row IN LISTS _st_rows)
        string(REPLACE "|" ";" _f "${_row}")
        list(GET _f 0 _key)
        list(LENGTH _f _n)
        if(NOT _key IN_LIST _known_keys)
            message(FATAL_ERROR "opencoti-fetch: ${_pin}: unknown KEY '${_key}'")
        endif()
        if(_key STREQUAL "file")
            if(_n LESS 6)
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: file needs <platform> <kind> <path> <sha256> <bytes>")
            endif()
            list(GET _f 1 _r_plat)
            list(GET _f 2 _r_kind)
            list(GET _f 3 _r_path)
            list(GET _f 4 _r_sha)
            list(GET _f 5 _r_bytes)
            if(NOT _r_plat IN_LIST _platforms AND NOT _r_plat STREQUAL "any")
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: unknown platform '${_r_plat}'")
            endif()
            if(NOT _r_sha MATCHES "${_hex64}" OR NOT _r_bytes MATCHES "^[0-9]+$")
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: bad sha256 or size for ${_r_path}")
            endif()
            if(_r_path MATCHES "^/" OR _r_path MATCHES "\\.\\.")
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: ${_r_path} is not a plain repo-relative path")
            endif()
            list(APPEND _p_plat "${_r_plat}")
            list(APPEND _p_kind "${_r_kind}")
            list(APPEND _p_path "${_r_path}")
            list(APPEND _p_sha "${_r_sha}")
            list(APPEND _p_bytes "${_r_bytes}")
        elseif(_key STREQUAL "abi")
            if(_n LESS 3)
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: abi needs <name> <digest>")
            endif()
            list(GET _f 1 _a_name)
            list(GET _f 2 _a_digest)
            list(APPEND _p_abis "${_a_name}=${_a_digest}")
        elseif(_key STREQUAL "component" OR _key STREQUAL "version" OR _key STREQUAL "repo"
               OR _key STREQUAL "rev" OR _key STREQUAL "engine-min")
            if(_n LESS 2)
                message(FATAL_ERROR "opencoti-fetch: ${_pin}: ${_key} needs a value")
            endif()
            string(REPLACE "-" "_" _var "${_key}")
            list(GET _f 1 _p_${_var})
        endif()
    endforeach()

    if(NOT _p_component STREQUAL "${_c}" OR NOT _p_version STREQUAL "${_ix_${_c}_version}")
        message(FATAL_ERROR
            "opencoti-fetch: the index names ${_c} ${_ix_${_c}_version}, ${_pin} is ${_p_component} ${_p_version}")
    endif()
    if(_p_repo STREQUAL "" OR NOT _p_rev MATCHES "${_hex40}")
        message(FATAL_ERROR "opencoti-fetch: ${_pin}: repo or rev missing, or rev is not a 40-character commit")
    endif()

    if(_c STREQUAL "engine")
        set(_engine_abis "${_p_abis}")
        set(_engine_version "${_p_version}")
    else()
        # Compatibility: every abi a component states is one the engine
        # provides, name and digest, and the engine is not older than the
        # component was built for. Build ids are equal-length decimal strings.
        foreach(_abi IN LISTS _p_abis)
            if(NOT _abi IN_LIST _engine_abis)
                message(FATAL_ERROR
                    "opencoti-fetch: ${_c} ${_p_version} needs abi ${_abi}, which engine ${_engine_version} does not provide (${_engine_abis})")
            endif()
        endforeach()
        if(_p_engine_min STREQUAL "" OR _engine_version STRLESS _p_engine_min)
            message(FATAL_ERROR
                "opencoti-fetch: ${_c} ${_p_version} needs engine ${_p_engine_min} or newer, the index names ${_engine_version}")
        endif()
    endif()

    # Which rows are this platform's. A component with no row for the platform
    # itself is not taken at all (its BUILD_INFO alone is not a payload); the
    # engine is one file for every platform, so it always is. A bin is the
    # platform's own row when there is one, else the `any` row.
    set(_has_own FALSE)
    set(_has_own_bin FALSE)
    list(LENGTH _p_plat _rows)
    if(_rows EQUAL 0)
        message(FATAL_ERROR "opencoti-fetch: ${_pin}: no file row")
    endif()
    math(EXPR _last "${_rows} - 1")
    foreach(_i RANGE ${_last})
        list(GET _p_plat ${_i} _r_plat)
        list(GET _p_kind ${_i} _r_kind)
        if(_r_plat STREQUAL "${ARCH}")
            set(_has_own TRUE)
            if(_r_kind STREQUAL "bin")
                set(_has_own_bin TRUE)
            endif()
        endif()
    endforeach()
    if(NOT _has_own AND NOT _c STREQUAL "engine")
        continue()
    endif()
    foreach(_i RANGE ${_last})
        list(GET _p_plat ${_i} _r_plat)
        list(GET _p_kind ${_i} _r_kind)
        if(NOT _r_plat STREQUAL "${ARCH}" AND NOT _r_plat STREQUAL "any")
            continue()
        endif()
        if(_r_kind STREQUAL "bin" AND _r_plat STREQUAL "any" AND _has_own_bin)
            continue()
        endif()
        list(GET _p_path ${_i} _r_path)
        list(GET _p_sha ${_i} _r_sha)
        list(GET _p_bytes ${_i} _r_bytes)
        if(_c STREQUAL "engine" AND _r_kind STREQUAL "bin")
            get_filename_component(_engine_name "${_r_path}" NAME)
        endif()
        list(APPEND _s_component "${_c}")
        list(APPEND _s_version "${_p_version}")
        list(APPEND _s_kind "${_r_kind}")
        list(APPEND _s_repo "${_p_repo}")
        list(APPEND _s_rev "${_p_rev}")
        list(APPEND _s_path "${_r_path}")
        list(APPEND _s_sha "${_r_sha}")
        list(APPEND _s_bytes "${_r_bytes}")
    endforeach()
endforeach()

if(_engine_name STREQUAL "")
    message(FATAL_ERROR "opencoti-fetch: ${PIN_DIR}/${_ix_engine_file} has no bin row for ${ARCH}")
endif()

# A dev pin is said out loud in the build log. Building against a moving engine
# is a deliberate choice and should never be something you have to go and check.
set(_chan_note "")
if(NOT _channel STREQUAL "release")
    set(_chan_note " [channel ${_channel}]")
endif()

# ---- staging -----------------------------------------------------------------
# TRUE when path holds exactly the pinned bytes.
function(_opencoti_verify path sha bytes result)
    set(${result} FALSE PARENT_SCOPE)
    if(NOT EXISTS "${path}")
        return()
    endif()
    file(SIZE "${path}" _size)
    if(NOT _size EQUAL "${bytes}")
        return()
    endif()
    file(SHA256 "${path}" _got)
    if(_got STREQUAL "${sha}")
        set(${result} TRUE PARENT_SCOPE)
    endif()
endfunction()

file(MAKE_DIRECTORY "${DEST_DIR}")

# What the classic pin staged and this one does not: the GPU libraries under
# the loader's bare names, the one BUILD_INFO.md, and the second engine beside
# the CUDA 12 library. A left-over library is one the engine may load instead
# of the pinned one, and nothing would say so.
foreach(_stale ggml-cuda.so ggml-vulkan.so ggml-cuda.dll ggml-vulkan.dll BUILD_INFO.md)
    if(EXISTS "${DEST_DIR}/${_stale}")
        message(STATUS "opencoti-llamafile: removing ${DEST_DIR}/${_stale}, staged by an earlier pin under a name this one does not use")
        file(REMOVE "${DEST_DIR}/${_stale}")
    endif()
endforeach()
foreach(_stale "${DEST_DIR}/engines/cuda_v12" "${DEST_DIR}/cuda_v12")
    if(IS_DIRECTORY "${_stale}")
        file(GLOB _stale_engines "${_stale}/opencoti-*")
        if(_stale_engines)
            message(STATUS "opencoti-llamafile: removing ${_stale}, the second engine an earlier pin staged for CUDA 12")
            file(REMOVE_RECURSE "${_stale}")
        endif()
    endif()
endforeach()

set(_manifest "")
list(LENGTH _s_path _count)
math(EXPR _last "${_count} - 1")
foreach(_i RANGE ${_last})
    list(GET _s_component ${_i} _c)
    list(GET _s_version ${_i} _version)
    list(GET _s_kind ${_i} _kind)
    list(GET _s_repo ${_i} _repo)
    list(GET _s_rev ${_i} _rev)
    list(GET _s_path ${_i} _path)
    list(GET _s_sha ${_i} _sha)
    list(GET _s_bytes ${_i} _bytes)

    get_filename_component(_published "${_path}" NAME)
    set(_name "${_published}")
    if(_kind STREQUAL "build-info")
        get_filename_component(_stem "${_published}" NAME_WLE)
        get_filename_component(_ext "${_published}" LAST_EXT)
        set(_name "${_stem}.${_c}${_ext}")
    endif()
    set(_dest "${DEST_DIR}/${_name}")
    set(_what "${_c} ${_version} ${_kind} ${_published}")

    _opencoti_verify("${_dest}" "${_sha}" "${_bytes}" _have)

    # A local file, still verified: "I pointed it at a file I had" is the
    # easiest way to ship the wrong cut of the engine.
    if(NOT _have)
        set(_local "")
        if(_c STREQUAL "engine" AND _kind STREQUAL "bin")
            if(DEFINED LOCAL_FILE AND NOT "${LOCAL_FILE}" STREQUAL "")
                set(_local "${LOCAL_FILE}")
                if(NOT EXISTS "${_local}")
                    message(FATAL_ERROR "opencoti-fetch: XOLLAMA_OPENCOTI_ENGINE_FILE=${_local} does not exist")
                endif()
            endif()
        elseif(DEFINED LOCAL_SIDECAR_DIR AND NOT "${LOCAL_SIDECAR_DIR}" STREQUAL "")
            set(_local "${LOCAL_SIDECAR_DIR}/${_name}")
            if(NOT EXISTS "${_local}")
                set(_local "${LOCAL_SIDECAR_DIR}/${_published}")
            endif()
            if(NOT EXISTS "${_local}")
                message(FATAL_ERROR "opencoti-fetch: LOCAL_SIDECAR_DIR=${LOCAL_SIDECAR_DIR} has no ${_published} (${_what})")
            endif()
        endif()
        if(NOT _local STREQUAL "")
            _opencoti_verify("${_local}" "${_sha}" "${_bytes}" _ok)
            if(NOT _ok)
                file(SHA256 "${_local}" _got)
                message(FATAL_ERROR
                    "opencoti-fetch: ${_local} does not match the pin (${_what}).\n"
                    "  expected ${_sha} (${_bytes} bytes)\n"
                    "  actual   ${_got}")
            endif()
            file(COPY_FILE "${_local}" "${_dest}" ONLY_IF_DIFFERENT)
            set(_have TRUE)
            message(STATUS "opencoti-llamafile (${ARCH}): ${_what} from verified local ${_local}")
        endif()
    endif()

    if(NOT _have)
        # Downloads go to .part, so an interrupted fetch cannot be mistaken
        # for a good file on the next build. With a cache, the cache holds the
        # download and the payload gets a copy.
        set(_target "${_dest}")
        if(DEFINED CACHE_DIR AND NOT "${CACHE_DIR}" STREQUAL "")
            file(MAKE_DIRECTORY "${CACHE_DIR}/${_c}-${_version}")
            set(_target "${CACHE_DIR}/${_c}-${_version}/${_published}")
        endif()
        _opencoti_verify("${_target}" "${_sha}" "${_bytes}" _cached)
        if(NOT _cached)
            set(_url "https://huggingface.co/${_repo}/resolve/${_rev}/${_path}")
            message(STATUS "opencoti-llamafile (${ARCH}): fetching ${_url}")
            file(DOWNLOAD "${_url}" "${_target}.part"
                EXPECTED_HASH "SHA256=${_sha}"
                TLS_VERIFY ON
                SHOW_PROGRESS
                STATUS _status)
            list(GET _status 0 _code)
            if(NOT _code EQUAL 0)
                list(GET _status 1 _message)
                file(REMOVE "${_target}.part")
                message(FATAL_ERROR
                    "opencoti-fetch: could not fetch ${_url}\n"
                    "  ${_message}\n"
                    "  Build offline with -DXOLLAMA_OPENCOTI_ENGINE_FILE=<engine> and "
                    "-DXOLLAMA_OPENCOTI_SIDECAR_DIR=<directory holding every other file>, "
                    "or without the engine with -DXOLLAMA_OPENCOTI_ENGINE=OFF.")
            endif()
            file(SIZE "${_target}.part" _size)
            if(NOT _size EQUAL "${_bytes}")
                file(REMOVE "${_target}.part")
                message(FATAL_ERROR "opencoti-fetch: ${_url} is ${_size} bytes, the pin says ${_bytes}")
            endif()
            file(RENAME "${_target}.part" "${_target}")
        endif()
        if(NOT _target STREQUAL "${_dest}")
            file(COPY_FILE "${_target}" "${_dest}" ONLY_IF_DIFFERENT)
        endif()
    endif()

    # The engine and the macOS loader are run, so the execute bit is part of
    # the payload; nothing else may have it, because on the dev channel that
    # bit is how llm/engine/opencoti.go (isArtifact) tells the engine from the
    # libraries beside it. install(DIRECTORY ... USE_SOURCE_PERMISSIONS) in
    # cmake/local.cmake carries it into the package from here.
    if(_kind STREQUAL "bin" OR _kind STREQUAL "ape")
        file(CHMOD "${_dest}" PERMISSIONS
            OWNER_READ OWNER_WRITE OWNER_EXECUTE
            GROUP_READ GROUP_EXECUTE
            WORLD_READ WORLD_EXECUTE)
    else()
        file(CHMOD "${_dest}" PERMISSIONS OWNER_READ OWNER_WRITE GROUP_READ WORLD_READ)
    endif()
    string(APPEND _manifest "${_c} ${_version} ${_kind} ${_name} ${_sha} ${_bytes}\n")
endforeach()

if(DEFINED MANIFEST AND NOT "${MANIFEST}" STREQUAL "")
    file(WRITE "${MANIFEST}" "${_manifest}")
endif()

message(STATUS "opencoti-llamafile ${_engine_name}${_chan_note} (${ARCH}): ${_count} files staged in ${DEST_DIR}")
