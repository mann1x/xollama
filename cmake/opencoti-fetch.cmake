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
set(_tag "")
set(_asset_path "")
set(_asset_sha "")

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
    elseif(_key STREQUAL "bin" AND _n EQUAL 4)
        list(GET _fields 1 _row_arch)
        if(_row_arch STREQUAL "${ARCH}")
            list(GET _fields 2 _asset_path)
            list(GET _fields 3 _asset_sha)
        endif()
    else()
        message(FATAL_ERROR "opencoti-fetch: cannot parse pin line: ${_line}")
    endif()
endforeach()

if(_repo STREQUAL "" OR _rev STREQUAL "")
    message(FATAL_ERROR "opencoti-fetch: ${PIN_FILE} is missing a repo or rev directive")
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

message(STATUS "opencoti-llamafile ${_tag} (${ARCH}) staged at ${_dest}")
