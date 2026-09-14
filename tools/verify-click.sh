#!/bin/bash
# Static checks on a built .click, before it ever reaches a device.
#
# Catches the failure modes that otherwise cost a full
# build-install-launch-read-the-journal cycle each:
#   - dangling symlinks (a library whose soname version differs from its file
#     version, so a naive glob ships the link but not the target)
#   - a NEEDED library that is neither bundled nor present on the device
#   - a binary built for the wrong architecture
#   - host build droppings shipped by accident (__pycache__, *.pyc)
#
# Usage: tools/verify-click.sh [path/to/.click]
# If a device is on adb, unbundled libraries are checked against it too;
# without one they are listed for review.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLICK=${1:-}
if [ -z "$CLICK" ]; then
    CLICK=$(ls -t "$ROOT"/build/*/app/*.click 2>/dev/null | head -1)
fi
[ -n "$CLICK" ] && [ -f "$CLICK" ] || { echo "no .click found; run clickable build first" >&2; exit 1; }
# ar is run from a scratch directory below, so a relative path would not resolve.
CLICK=$(readlink -f "$CLICK")

WANT_ARCH=$(basename "$CLICK" | sed -E 's/.*_([a-z0-9]+)\.click$/\1/')
case "$WANT_ARCH" in
    arm64) WANT_MACHINE="AArch64"; TRIPLET=aarch64-linux-gnu ;;
    amd64) WANT_MACHINE="X86-64";  TRIPLET=x86_64-linux-gnu ;;
    armhf) WANT_MACHINE="ARM";     TRIPLET=arm-linux-gnueabihf ;;
    *) echo "unknown architecture '$WANT_ARCH'" >&2; exit 1 ;;
esac

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
( cd "$TMP" && ar x "$CLICK" && mkdir -p x && tar xf data.tar.* -C x ) \
    || { echo "could not extract $CLICK" >&2; exit 1; }
X="$TMP/x"
# Guard against the worst failure mode for a checker: passing every test
# because it is inspecting an empty directory.
[ -n "$(ls -A "$X" 2>/dev/null)" ] \
    || { echo "extracted click is empty -- refusing to report a pass" >&2; exit 1; }

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  [PASS] %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  [FAIL] %s\n' "$1"; }

echo "Verifying $(basename "$CLICK") (arch $WANT_ARCH)"
echo

# --- symlinks must resolve inside the click -------------------------------
dangling=""
while IFS= read -r link; do
    [ -e "$link" ] || dangling="$dangling $(basename "$link") -> $(readlink "$link")"
done < <(find "$X" -type l)
if [ -n "$dangling" ]; then
    bad "dangling symlinks:$dangling"
else
    ok "every symlink resolves inside the click"
fi

# --- architecture of every ELF --------------------------------------------
wrong=""
while IFS= read -r f; do
    head -c4 "$f" | grep -q ELF || continue
    m=$(readelf -h "$f" 2>/dev/null | awk -F: '/Machine:/{gsub(/^ +/,"",$2); print $2}')
    case "$m" in *"$WANT_MACHINE"*) ;; *) wrong="$wrong ${f#$X/}($m)" ;; esac
done < <(find "$X" -type f)
if [ -n "$wrong" ]; then bad "wrong architecture:$wrong"; else ok "every ELF is $WANT_MACHINE"; fi

# --- build droppings -------------------------------------------------------
junk=$(find "$X" \( -name '__pycache__' -o -name '*.pyc' -o -name '*.pyo' \) | sed "s|$X/||" | tr '\n' ' ')
if [ -n "$junk" ]; then bad "host build droppings shipped: $junk"; else ok "no __pycache__ or .pyc shipped"; fi

# --- the desktop Exec target must exist ------------------------------------
EXEC=$(awk -F= '/^Exec=/{print $2}' "$X"/*.desktop 2>/dev/null | awk '{print $1}' | sed 's|^\./||')
if [ -n "$EXEC" ] && [ -x "$X/$EXEC" ]; then
    ok "desktop Exec target exists and is executable ($EXEC)"
else
    bad "desktop Exec target missing or not executable: '$EXEC'"
fi

# --- NEEDED closure --------------------------------------------------------
needed=$(while IFS= read -r f; do
    head -c4 "$f" | grep -q ELF || continue
    readelf -d "$f" 2>/dev/null | sed -n 's/.*NEEDED.*\[\(.*\)\]/\1/p'
done < <(find "$X" -type f) | sort -u)

bundled=$(cd "$X/lib/$TRIPLET" 2>/dev/null && ls | sort -u)
unbundled=""
for n in $needed; do
    printf '%s\n' "$bundled" | grep -qx "$n" || unbundled="$unbundled $n"
done

if [ -z "$unbundled" ]; then
    ok "every NEEDED library is bundled"
elif adb get-state >/dev/null 2>&1; then
    missing=""
    for n in $unbundled; do
        found=$(adb shell "ls /usr/lib/$TRIPLET/$n /lib/$TRIPLET/$n 2>/dev/null | head -1" | tr -d '\r')
        [ -z "$found" ] && missing="$missing $n"
    done
    if [ -n "$missing" ]; then
        bad "NEEDED but neither bundled nor on the device:$missing"
    else
        ok "every unbundled NEEDED library is present on the device"
    fi
else
    echo "  [ .. ] no device on adb; these are expected from the rootfs, verify before release:"
    for n in $unbundled; do echo "         $n"; done
fi

echo
echo "-- $pass passed, $fail failed --"
[ "$fail" -eq 0 ]
