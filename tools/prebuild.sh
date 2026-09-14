#!/bin/bash
# Build the three native components of the click for the target architecture.
# Runs INSIDE the clickable build container (clickable.yaml's prebuild hook),
# which is an amd64 host carrying an aarch64 cross-toolchain.
#
#   prebuild.sh <ARCH> <ARCH_TRIPLET> <BUILD_DIR>
#
# Produces <ROOT>/build/native/<TRIPLET>/{ocbridge,openvpn,openconnect}, which
# CMake then installs side by side into the click's bin directory. ocbridge
# locates the other two relative to its own executable, so they must stay
# together.
set -euo pipefail

ARCH=${1:?arch}
TRIPLET=${2:?arch triplet}
BUILD_DIR=${3:?build dir}
ROOT=$(cd "$(dirname "$0")/.." && pwd)

OPENVPN_VERSION=2.6.19
OPENVPN_URL="https://swupdate.openvpn.org/community/releases/openvpn-${OPENVPN_VERSION}.tar.gz"
OPENVPN_SHA256=13702526f687c18b2540c1a3f2e189187baaa65211edcf7ff6772fa69f0536cf

# 9.12 is the version validated against a real Cisco gateway; see
# tasks/lessons.md. It needs --useragent at runtime or newer Cisco heads 404 its
# XML-POST auth and silently fall back to a legacy path.
OPENCONNECT_VERSION=9.12
OPENCONNECT_URL="https://www.infradead.org/openconnect/download/openconnect-${OPENCONNECT_VERSION}.tar.gz"
OPENCONNECT_SHA256=a2bedce3aa4dfe75e36e407e48e8e8bc91d46def5335ac9564fbf91bd4b2413e

OUT="${ROOT}/build/native/${TRIPLET}"
CACHE="${ROOT}/build/native-src"
mkdir -p "$OUT" "$CACHE"

case "$ARCH" in
    arm64) GOARCH=arm64 ;;
    amd64) GOARCH=amd64 ;;
    armhf) GOARCH=arm ;;
    *) echo "prebuild: unsupported arch $ARCH" >&2; exit 1 ;;
esac

CROSS_ARGS=()
STRIP=strip
if [ "$(dpkg --print-architecture)" != "$ARCH" ]; then
    # Point pkg-config at the target's .pc files, or configure silently finds
    # the host's amd64 libraries and the link fails much later with something
    # that does not mention architecture at all.
    export PKG_CONFIG_LIBDIR="/usr/lib/${TRIPLET}/pkgconfig:/usr/share/pkgconfig"
    CROSS_ARGS=(--host="$TRIPLET")
    STRIP="${TRIPLET}-strip"
fi

fetch() {   # fetch <url> <tarball> <sha256>
    local url=$1 tarball=$2 want=$3 path="$CACHE/$2"
    if [ ! -f "$path" ]; then
        echo "prebuild: fetching $tarball"
        curl -sSL -o "$path.part" "$url"
        mv "$path.part" "$path"
    fi
    local got
    got=$(sha256sum "$path" | cut -d' ' -f1)
    if [ "$got" != "$want" ]; then
        echo "prebuild: checksum mismatch for $tarball" >&2
        echo "  expected $want" >&2
        echo "  got      $got" >&2
        exit 1
    fi
}

unpack() {  # unpack <tarball> <dir> -> clean source tree
    rm -rf "${CACHE:?}/$2"
    tar xzf "$CACHE/$1" -C "$CACHE"
}

# ---------------------------------------------------------------- ocbridge --
# CGO_ENABLED=0 gives a static binary with no library closure to bundle, and
# makes Go's net package use its pure-Go resolver. ocbridge never resolves a
# name anyway: it speaks only to descriptors and loopback.
echo "prebuild: building ocbridge for $GOARCH"
( cd "$ROOT/src/ocbridge"
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
    GOCACHE="${BUILD_DIR}/.gocache" GOFLAGS=-trimpath \
    go build -ldflags "-s -w" -o "$OUT/ocbridge" . )

# ----------------------------------------------------------------- openvpn --
# Patched to accept an inherited descriptor as its tun device, so it can serve
# without /dev/net/tun and without root. DCO must stay disabled: with kernel
# offload openvpn never touches that descriptor.
fetch "$OPENVPN_URL" "openvpn-${OPENVPN_VERSION}.tar.gz" "$OPENVPN_SHA256"
unpack "openvpn-${OPENVPN_VERSION}.tar.gz" "openvpn-${OPENVPN_VERSION}"
( cd "$CACHE/openvpn-${OPENVPN_VERSION}"
  patch -p1 --fuzz=0 < "$ROOT/packaging/openvpn/0001-dev-node-fd.patch"
  echo "prebuild: building openvpn ${OPENVPN_VERSION}"
  ./configure "${CROSS_ARGS[@]}" \
      --disable-lzo --disable-lz4 --disable-plugin-auth-pam \
      --disable-dco --disable-systemd --disable-selinux \
      --disable-iproute2 --disable-dependency-tracking \
      > "$BUILD_DIR/openvpn-configure.log" 2>&1 \
      || { tail -30 "$BUILD_DIR/openvpn-configure.log"; exit 1; }
  make -j"$(nproc)" > "$BUILD_DIR/openvpn-make.log" 2>&1 \
      || { tail -30 "$BUILD_DIR/openvpn-make.log"; exit 1; }
  "$STRIP" src/openvpn/openvpn
  cp src/openvpn/openvpn "$OUT/openvpn" )

# ------------------------------------------------------------- openconnect --
# Built from source rather than taken from the distribution package, purely to
# cut the dependency closure. Ubuntu's openconnect links libproxy, whose PAC
# backend drags in libcurl-gnutls and libduktape and, transitively, some thirty
# libraries (brotli, ldap, sasl, ssh, rtmp, nghttp2, ...). Nothing in this app
# uses a proxy auto-config script.
#
# Dropping libproxy, stoken (RSA SecurID), libpskc (OATH), pcsclite (smartcard)
# and GSSAPI, and linking libopenconnect statically, leaves ONE binary that
# needs only base-system libraries: libssl, libcrypto, libxml2, liblz4, libz,
# libm, libc. So the click bundles no shared libraries at all.
#
# OpenSSL rather than GnuTLS so openconnect and openvpn share one TLS stack.
# --with-vpnc-script is set to a path that is never used: the app always runs
# openconnect with --script-tun, which replaces the script entirely.
#
# It is also patched so --script-tun execs a plain program directly rather than
# through /bin/sh, which confinement forbids.
fetch "$OPENCONNECT_URL" "openconnect-${OPENCONNECT_VERSION}.tar.gz" "$OPENCONNECT_SHA256"
unpack "openconnect-${OPENCONNECT_VERSION}.tar.gz" "openconnect-${OPENCONNECT_VERSION}"
( cd "$CACHE/openconnect-${OPENCONNECT_VERSION}"
  # Without this, --script-tun is unusable under AppArmor confinement:
  # openconnect always spawns the script through /bin/sh, which a click may not
  # execute. See packaging/openconnect/README.md.
  patch -p1 --fuzz=0 < "$ROOT/packaging/openconnect/0001-script-tun-direct-exec.patch"
  echo "prebuild: building openconnect ${OPENCONNECT_VERSION}"
  ./configure "${CROSS_ARGS[@]}" \
      --without-gnutls --without-libproxy --without-stoken \
      --without-libpcsclite --without-libpskc --without-gssapi \
      --disable-nls --disable-shared --enable-static \
      --with-vpnc-script=/bin/true --disable-dependency-tracking \
      > "$BUILD_DIR/openconnect-configure.log" 2>&1 \
      || { tail -30 "$BUILD_DIR/openconnect-configure.log"; exit 1; }
  make -j"$(nproc)" > "$BUILD_DIR/openconnect-make.log" 2>&1 \
      || { tail -30 "$BUILD_DIR/openconnect-make.log"; exit 1; }
  "$STRIP" openconnect
  cp openconnect "$OUT/openconnect" )

echo "prebuild: done"
ls -la "$OUT"
