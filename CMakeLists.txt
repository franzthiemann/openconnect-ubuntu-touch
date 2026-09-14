cmake_minimum_required(VERSION 3.16)
project(ocvpn C CXX)

# Clickable exports ARCH (arm64/amd64/armhf), CLICK_FRAMEWORK and
# APPARMOR_POLICY. Note the variable is ARCH, not CLICK_ARCH. Everything is
# defaulted so a bare `cmake` still configures.
set(CLICK_ARCH "$ENV{ARCH}")
if(CLICK_ARCH STREQUAL "")
    set(CLICK_ARCH "arm64")
endif()
set(CLICK_FRAMEWORK "$ENV{CLICK_FRAMEWORK}")
if(CLICK_FRAMEWORK STREQUAL "")
    set(CLICK_FRAMEWORK "ubuntu-touch-24.04-1.x")
endif()
set(APPARMOR_POLICY "$ENV{APPARMOR_POLICY}")
if(APPARMOR_POLICY STREQUAL "")
    set(APPARMOR_POLICY "2404.1")
endif()

set(ARCH_TRIPLET "$ENV{ARCH_TRIPLET}")
if(ARCH_TRIPLET STREQUAL "")
    execute_process(COMMAND dpkg-architecture -qDEB_HOST_MULTIARCH
                    OUTPUT_VARIABLE ARCH_TRIPLET
                    OUTPUT_STRIP_TRAILING_WHITESPACE)
endif()

set(FULL_PROJECT_NAME "ocvpn.franzthiemann")
set(DATA_DIR .)
set(BIN_DIR "lib/${ARCH_TRIPLET}/bin")

configure_file(manifest.json.in    ${CMAKE_CURRENT_BINARY_DIR}/manifest.json)
configure_file(ocvpn.apparmor.in   ${CMAKE_CURRENT_BINARY_DIR}/ocvpn.apparmor)
configure_file(ocvpn.desktop.in    ${CMAKE_CURRENT_BINARY_DIR}/ocvpn.desktop)
install(FILES ${CMAKE_CURRENT_BINARY_DIR}/manifest.json
              ${CMAKE_CURRENT_BINARY_DIR}/ocvpn.apparmor
              ${CMAKE_CURRENT_BINARY_DIR}/ocvpn.desktop
        DESTINATION ${DATA_DIR})

install(DIRECTORY qml    DESTINATION ${DATA_DIR})
install(DIRECTORY assets DESTINATION ${DATA_DIR})
# Go sources are built by the prebuild hook, not shipped. __pycache__ must be
# excluded too: it is whatever the HOST's python left behind (3.14 here), which
# is both useless and misleading on a device running 3.12.
install(DIRECTORY src DESTINATION ${DATA_DIR}
        PATTERN "ocbridge" EXCLUDE
        REGEX "__pycache__" EXCLUDE
        REGEX "\\.pyc$" EXCLUDE)

configure_file(bin/ocvpn-launch.in ${CMAKE_CURRENT_BINARY_DIR}/ocvpn-launch @ONLY)
install(PROGRAMS ${CMAKE_CURRENT_BINARY_DIR}/ocvpn-launch
        DESTINATION ${BIN_DIR})

# Built by tools/prebuild.sh (clickable's prebuild hook) before CMake runs:
# ocbridge cross-compiled by Go, and the patched openvpn cross-compiled by the
# container's aarch64 toolchain.
set(NATIVE_DIR "${CMAKE_CURRENT_SOURCE_DIR}/build/native/${ARCH_TRIPLET}")
foreach(prog ocbridge openvpn openconnect)
    if(NOT EXISTS "${NATIVE_DIR}/${prog}")
        message(FATAL_ERROR
            "${NATIVE_DIR}/${prog} is missing. It is produced by "
            "tools/prebuild.sh, which clickable runs as the prebuild hook. "
            "If you invoked cmake directly, run that script first.")
    endif()
endforeach()
install(PROGRAMS "${NATIVE_DIR}/ocbridge"
                 "${NATIVE_DIR}/openvpn"
                 "${NATIVE_DIR}/openconnect"
        DESTINATION ${BIN_DIR})

file(GLOB_RECURSE PROJECT_SRC_FILES RELATIVE ${CMAKE_CURRENT_SOURCE_DIR}
     qml/*.qml qml/*.js src/*.py *.json.in *.apparmor.in *.desktop.in)
add_custom_target(${PROJECT_NAME}_FILES ALL SOURCES ${PROJECT_SRC_FILES})
