#!/bin/bash
# Run the integration tests inside an Ubuntu 24.04 userspace, against the
# binaries from a built click.
#
# The Manjaro host cannot run them: the click's openconnect links Ubuntu's
# libxml2.so.2, and Manjaro's libxml2 has a different soname. Running in a
# container matching the device's userspace is both the fix and the more honest
# test.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
BIN=${1:-$ROOT/build/x86_64-linux-gnu/app/install/lib/x86_64-linux-gnu/bin}

[ -x "$BIN/ocbridge" ] || { echo "no binaries at $BIN; run 'clickable build --arch amd64'" >&2; exit 1; }

"$HERE/rig.sh" up
trap '"$HERE/rig.sh" down >/dev/null 2>&1 || true' EXIT

run_suite() {
    echo
    echo "=== $1"
    docker run --rm --network host \
        -v "$ROOT:/work:ro" -v "$BIN:/bin-under-test:ro" \
        -e OCUBT_SKIP_RIG=1 -e OCUBT_BIN_DIR=/bin-under-test \
        -w /work ocubt-rig python3 "$1"
}

rc=0
run_suite tests/rig/e2e_test.py  || rc=1
run_suite tests/test_backend.py  || rc=1
exit $rc
