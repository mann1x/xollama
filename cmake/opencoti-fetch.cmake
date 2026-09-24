# cmake/opencoti-fetch.cmake — resolve one pinned opencoti-llamafile artifact
# into the runtime payload. Run as a script (`cmake -P`), never included.
#
# This is the whole of xollama's engine acquisition, and it happens exactly
# once, at BUILD time. The runtime never downloads an engine: a model load must
# not be able to stall on a 650 MB fetch, and an installed package must work
# with no network at all. llm/engine/opencoti.go only ever looks for an
# artifact that is already on disk.
#
# Required:
#   -DPIN_FILE=<path>   llm/engine/pin.txt
#   -DARCH=<label>      arch label to resolve (x86_64, aarch64, win-x86_64-gpu…)
#   -DDEST=<path>       where to stage the artifact
# Optional:
#   -DLOCAL_FILE=<path> stage this file instead of downloading (still verified)
#   -DCACHE_DIR=<path>  keep the download here so a clean rebuild is free
#
# The pinned SHA256 is enforced on every path, including -DLOCAL_FILE. A
# mismatch is a hard error: shipping an unverified inference engine inside an
# installer is not a thing to warn about and continue past.

cmake_minimum_required(VERSION 3.24)

foreach(_required PIN_FILE ARCH DEST_DIR)
    if(NOT DEFINED ${_required} OR "${${_required}}" STREQUAL "")
        message(FATAL_ERROR "opencoti-fetch: -D${_required}=<value> is required")
    endif()
endforeach()

if(NOT EXISTS "${PIN_FILE}")
    message(FATAL_ERROR "opencoti-fetch: no pin file at ${PIN_FILE}")
endif()

# Parse pin.txt. The format is kept trivial precisely so this parser and the Go
# one in llm/engine/pin.go cannot drift; llm/engine/pin_test.go
# (TestPinFormatIsWhatCMakeParses) pins the assumptions made here.
set(_repo "")
set(_rev "")
set(_channel "")
set(_tag "")
set(_asset_path "")
set(_asset_sha "")
set(_dso_paths "")
set(_dso_shas "")
set(_dso_labels "")

file(STRINGS "${PIN_FILE}" _lines)
foreach(_line IN LISTS _lines)
    string(STRIP "${_line}" _line)
    # Drop whole-line comments before any regex touches them. CMake's regex "."
    # does not match the bytes of a multi-byte UTF-8 character, so "#.*$" alone
    # silently leaves the tail of a comment containing one, and that tail then
    # fails to parse as a directive. Prose lives in whole-line comments, so
    # handling them first makes the parser indifferent to their contents.
    if(_line MATCHES "^#")
        continue()
    endif()
    string(REGEX REPLACE "#.*$" "" _line "${_line}")
    string(STRIP "${_line}" _line)
    if(_line STREQUAL "")
        continue()
    endif()
    separate_arguments(_fields UNIX_COMMAND "${_line}")
    list(GET _fields 0 _key)
    list(LENGTH _fields _n)
    if(_key STREQUAL "repo" AND _n EQUAL 2)
        list(GET _fields 1 _repo)
    elseif(_key STREQUAL "rev" AND _n EQUAL 2)
        list(GET _fields 1 _rev)
    elseif(_key STREQUAL "tag" AND _n EQUAL 2)
        list(GET _fields 1 _tag)
    elseif(_key STREQUAL "channel" AND _n EQUAL 2)
        list(GET _fields 1 _channel)
    elseif(_key STREQUAL "feature" AND _n EQUAL 2)
        # Declared engine capabilities. Only the Go side gates on these; they
        # are read here so that adding one cannot fail the build, and so the
        # two parsers stay able to read the same file. See llm/engine/pin.go.
    elseif(_key STREQUAL "accel" AND _n EQUAL 3)
        # Declared accelerator coverage. Routing-time fact, gated in Go; read
        # here only so an accel row cannot fail the build.
    elseif(_key STREQUAL "bin" AND _n EQUAL 4)
        list(GET _fields 1 _row_arch)
        if(_row_arch STREQUAL "${ARCH}")
            list(GET _fields 2 _asset_path)
            list(GET _fields 3 _asset_sha)
        endif()
    elseif(_key STREQUAL "dso" AND _n EQUAL 4)
        # Side-loadable GPU payload. A release bin embeds its own and carries
        # no dso row; a dev snapshot is a bare APE and this is the only way it
        # reaches a GPU. It is staged beside the binary because the
        # executable's own directory wins the engine's DSO search.
        #
        # ONE ARCH CAN HAVE SEVERAL. The label is <arch>[-<backend>]: the bare
        # form is CUDA, and a suffixed one names another backend for the same
        # arch (x86_64-vulkan). Matching only the bare label silently dropped
        # the Vulkan payload of snapshot 2609242056001 -- the build succeeded,
        # the engine shipped, and Vulkan loads ran on the CPU.
        list(GET _fields 1 _row_arch)
        if(_row_arch STREQUAL "${ARCH}" OR _row_arch MATCHES "^${ARCH}-")
            list(GET _fields 2 _row_path)
            list(GET _fields 3 _row_sha)
            list(APPEND _dso_paths "${_row_path}")
            list(APPEND _dso_shas "${_row_sha}")
            list(APPEND _dso_labels "${_row_arch}")
        endif()
    else()
        message(FATAL_ERROR "opencoti-fetch: cannot parse pin line: ${_line}")
    endif()
endforeach()

if(_repo STREQUAL "" OR _rev STREQUAL "")
    message(FATAL_ERROR "opencoti-fetch: ${PIN_FILE} is missing a repo or rev directive")
endif()

# A dev pin is said out loud in the build log. Building against a moving engine
# is a deliberate choice and should never be something you have to go and check.
set(_chan_note "")
if(NOT _channel STREQUAL "release")
    set(_chan_note " [channel ${_channel}: ${_repo}]")
endif()
if(_asset_path STREQUAL "")
    message(FATAL_ERROR "opencoti-fetch: ${PIN_FILE} has no 'bin ${ARCH}' row")
endif()

get_filename_component(_name "${_asset_path}" NAME)

function(_opencoti_verify path expected result)
    file(SHA256 "${path}" _got)
    if(_got STREQUAL "${expected}")
        set(${result} TRUE PARENT_SCOPE)
    else()
        set(${result} FALSE PARENT_SCOPE)
    endif()
endfunction()

if(DEFINED LOCAL_FILE AND NOT "${LOCAL_FILE}" STREQUAL "")
    # Explicit local artifact. Still verified: "I pointed it at a file I had"
    # is the easiest way to ship the wrong cut of the engine.
    if(NOT EXISTS "${LOCAL_FILE}")
        message(FATAL_ERROR "opencoti-fetch: XOLLAMA_OPENCOTI_ENGINE_FILE=${LOCAL_FILE} does not exist")
    endif()
    _opencoti_verify("${LOCAL_FILE}" "${_asset_sha}" _ok)
    if(NOT _ok)
        file(SHA256 "${LOCAL_FILE}" _got)
        message(FATAL_ERROR
            "opencoti-fetch: ${LOCAL_FILE} does not match the pin for ${ARCH}.\n"
            "  expected ${_asset_sha}\n"
            "  actual   ${_got}\n"
            "  pinned release: ${_tag}")
    endif()
    set(_source "${LOCAL_FILE}")
    message(STATUS "opencoti-llamafile ${_tag} (${ARCH}): using verified local ${LOCAL_FILE}")
else()
    if(NOT DEFINED CACHE_DIR OR "${CACHE_DIR}" STREQUAL "")
        set(CACHE_DIR "${DEST_DIR}")
    endif()
    file(MAKE_DIRECTORY "${CACHE_DIR}")
    set(_cached "${CACHE_DIR}/${_name}")

    set(_have FALSE)
    if(EXISTS "${_cached}")
        _opencoti_verify("${_cached}" "${_asset_sha}" _have)
        if(NOT _have)
            message(STATUS "opencoti-llamafile: cached ${_name} does not match the pin, refetching")
            file(REMOVE "${_cached}")
        endif()
    endif()

    if(NOT _have)
        set(_url "https://huggingface.co/${_repo}/resolve/${_rev}/${_asset_path}")
        message(STATUS "opencoti-llamafile ${_tag} (${ARCH}): fetching ${_url}")
        # EXPECTED_HASH makes this fail rather than write a bad file. Download
        # to .part so an interrupted fetch cannot be mistaken for a good cache
        # entry on the next build.
        file(DOWNLOAD "${_url}" "${_cached}.part"
            EXPECTED_HASH "SHA256=${_asset_sha}"
            TLS_VERIFY ON
            SHOW_PROGRESS
            STATUS _status)
        list(GET _status 0 _code)
        if(NOT _code EQUAL 0)
            list(GET _status 1 _message)
            file(REMOVE "${_cached}.part")
            message(FATAL_ERROR
                "opencoti-fetch: could not fetch ${_url}\n"
                "  ${_message}\n"
                "  Build offline with -DXOLLAMA_OPENCOTI_ENGINE_FILE=<artifact>, "
                "or without the engine with -DXOLLAMA_OPENCOTI_ENGINE=OFF.")
        endif()
        file(RENAME "${_cached}.part" "${_cached}")
    endif()
    set(_source "${_cached}")
endif()

set(_dest "${DEST_DIR}/${_name}")
file(MAKE_DIRECTORY "${DEST_DIR}")
file(COPY_FILE "${_source}" "${_dest}" ONLY_IF_DIFFERENT)

# The artifact is an APE binary that is executed directly, so the execute bit
# is part of the payload. install(DIRECTORY ... USE_SOURCE_PERMISSIONS) in
# cmake/local.cmake carries it into the package from here.
file(CHMOD "${_dest}" PERMISSIONS
    OWNER_READ OWNER_WRITE OWNER_EXECUTE
    GROUP_READ GROUP_EXECUTE
    WORLD_READ WORLD_EXECUTE)

# Stage the side-loadable GPU payload, when the pin carries one for this arch.
# Without it a dev snapshot still runs -- on the CPU, saying nothing -- which is
# why the Go side refuses to route accelerators to a pin with no accel rows.
list(LENGTH _dso_paths _dso_count)
set(_dso_index 0)
while(_dso_index LESS _dso_count)
    list(GET _dso_paths ${_dso_index} _dso_path)
    list(GET _dso_shas ${_dso_index} _dso_sha)
    list(GET _dso_labels ${_dso_index} _dso_label)
    math(EXPR _dso_index "${_dso_index} + 1")

    get_filename_component(_dso_name "${_dso_path}" NAME)
    set(_dso_dest "${DEST_DIR}/${_dso_name}")

    set(_dso_have FALSE)
    if(EXISTS "${_dso_dest}")
        _opencoti_verify("${_dso_dest}" "${_dso_sha}" _dso_have)
    endif()
    # LOCAL_DSO_FILE names a single file, so it can only stand in for the
    # arch's primary (bare-label, CUDA) payload. Any other backend's payload is
    # still fetched, rather than being silently satisfied by the wrong bytes.
    if(NOT _dso_have AND DEFINED LOCAL_DSO_FILE AND NOT "${LOCAL_DSO_FILE}" STREQUAL ""
       AND _dso_label STREQUAL "${ARCH}")
        if(NOT EXISTS "${LOCAL_DSO_FILE}")
            message(FATAL_ERROR "opencoti-fetch: LOCAL_DSO_FILE=${LOCAL_DSO_FILE} does not exist")
        endif()
        _opencoti_verify("${LOCAL_DSO_FILE}" "${_dso_sha}" _dso_ok)
        if(NOT _dso_ok)
            file(SHA256 "${LOCAL_DSO_FILE}" _dso_got)
            message(FATAL_ERROR
                "opencoti-fetch: ${LOCAL_DSO_FILE} does not match the dso pin for ${ARCH}.\n"
                "  expected ${_dso_sha}\n"
                "  actual   ${_dso_got}")
        endif()
        file(COPY_FILE "${LOCAL_DSO_FILE}" "${_dso_dest}" ONLY_IF_DIFFERENT)
        set(_dso_have TRUE)
    endif()
    if(NOT _dso_have)
        set(_dso_url "https://huggingface.co/${_repo}/resolve/${_rev}/${_dso_path}")
        message(STATUS "opencoti-llamafile ${_tag} (${ARCH}): fetching ${_dso_url}")
        file(DOWNLOAD "${_dso_url}" "${_dso_dest}.part"
            EXPECTED_HASH "SHA256=${_dso_sha}"
            TLS_VERIFY ON
            SHOW_PROGRESS
            STATUS _dso_status)
        list(GET _dso_status 0 _dso_code)
        if(NOT _dso_code EQUAL 0)
            list(GET _dso_status 1 _dso_message)
            file(REMOVE "${_dso_dest}.part")
            message(FATAL_ERROR
                "opencoti-fetch: could not fetch ${_dso_url}\n"
                "  ${_dso_message}\n"
                "  Build offline with -DLOCAL_DSO_FILE=<payload>.")
        endif()
        file(RENAME "${_dso_dest}.part" "${_dso_dest}")
    endif()
    message(STATUS "opencoti-llamafile ${_tag} (${_dso_label}) GPU payload staged at ${_dso_dest}")
endwhile()

message(STATUS "opencoti-llamafile ${_tag}${_chan_note} (${ARCH}) staged at ${_dest}")
