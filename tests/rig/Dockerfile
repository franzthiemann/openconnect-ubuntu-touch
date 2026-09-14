# A throwaway Cisco AnyConnect-compatible server (ocserv) for desktop testing,
# so nothing has to be installed on the host and no real VPN credentials are
# needed to exercise the openconnect side of the bridge.
#
# Ubuntu 24.04 on purpose: it is the same base as the UT 24.04 rootfs, so the
# openconnect 9.12 in here is the exact build the click will bundle.
#
# It doubles as the runner for the end-to-end test: running that inside an
# Ubuntu 24.04 userspace exercises the click's binaries against the same
# library versions the device has, which the Manjaro host cannot provide
# (its libxml2 has a different soname entirely).
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
        ocserv gnutls-bin iproute2 iputils-ping openconnect ca-certificates \
        python3 \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /etc/ocserv

# Self-signed server certificate. The CN is what --servercert pins against.
RUN certtool --generate-privkey --outfile server-key.pem 2>/dev/null \
 && printf 'cn = "ocserv-rig"\norganization = "openconnect_ubt test rig"\nserial = 001\nexpiration_days = 3650\nsigning_key\nencryption_key\ntls_www_server\n' > /tmp/server.tmpl \
 && certtool --generate-self-signed --load-privkey server-key.pem \
             --template /tmp/server.tmpl --outfile server-cert.pem 2>/dev/null \
 && rm /tmp/server.tmpl

# Test account: testuser / testpass
RUN printf 'testpass\ntestpass\n' | ocpasswd -c /etc/ocserv/ocpasswd testuser

COPY ocserv.conf /etc/ocserv/ocserv.conf

EXPOSE 4443/tcp 4443/udp
CMD ["ocserv", "-f", "-c", "/etc/ocserv/ocserv.conf", "-d", "1"]
