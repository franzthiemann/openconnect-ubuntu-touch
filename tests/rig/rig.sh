#!/bin/bash
# Start/stop the local AnyConnect test server, so the bridge can be developed
# without a real VPN account.
#
#   ./rig.sh build | up | down | fingerprint | logs
#
# Credentials: testuser / testpass, on https://localhost:4443
# NOTE: BuildKit is broken on this machine (EUCLEAN); the legacy builder works.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)

case "${1:-up}" in
build) DOCKER_BUILDKIT=0 docker build -t ocubt-rig "$HERE" ;;
up)
    docker rm -f ocubt-rig >/dev/null 2>&1 || true
    docker run -d --name ocubt-rig --cap-add NET_ADMIN --device /dev/net/tun \
        -p 4443:4443/tcp -p 4443:4443/udp ocubt-rig >/dev/null
    # Wait for ocserv to actually listen: `docker run -d` returns as soon as the
    # container is created, so probing immediately gets connection-refused and
    # looks like a broken rig.
    i=0
    while [ $i -lt 60 ]; do
        if (exec 3<>/dev/tcp/127.0.0.1/4443) 2>/dev/null; then
            exec 3>&- 2>/dev/null || true
            echo "rig up on https://localhost:4443 (testuser/testpass)"
            exit 0
        fi
        i=$((i + 1))
        sleep 0.25
    done
    echo "rig did not start listening on 4443" >&2
    docker logs ocubt-rig >&2 || true
    exit 1
    ;;
down) docker rm -f ocubt-rig >/dev/null 2>&1 || true; echo "rig down" ;;
logs) docker logs -f ocubt-rig ;;
fingerprint)
    # The pin openconnect prints when it cannot verify the self-signed cert;
    # this is exactly the string the app's TOFU flow scrapes from stderr.
    printf 'testpass\n' | openconnect --protocol=anyconnect --authenticate \
        --user=testuser --passwd-on-stdin --non-inter https://localhost:4443 2>&1 \
        | grep -oE -- '--servercert \S+' | awk '{print $2}'
    ;;
*) echo "usage: $0 build|up|down|logs|fingerprint" >&2; exit 2 ;;
esac
