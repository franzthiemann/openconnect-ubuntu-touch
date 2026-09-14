#!/usr/bin/env python3
"""Phase 0.4/0.6 spike: prove `--dev-node fd:N` carries traffic.

Runs a patched openvpn TLS *server* and a patched openvpn *client*, each with
its tun device replaced by an inherited SOCK_DGRAM socketpair. Nothing here
touches /dev/net/tun and nothing needs root, so this is exactly the situation
inside the confined click.

Then injects an IP packet into the client's fd and checks it comes out of the
server's fd, which is the ocbridge data path end to end.
"""
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
OPENVPN = os.path.join(os.path.dirname(HERE), "..", "packaging", "openvpn", "out", "openvpn")
OPENVPN = os.path.abspath(OPENVPN)
PORT = 11194
SERVER_IP, CLIENT_IP = "10.99.0.1", "10.99.0.121"

fails = []


def check(label, ok, detail=""):
    print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f" -- {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def run(*a, **kw):
    return subprocess.run(a, check=True, capture_output=True, **kw)


def make_pki(d):
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "3650",
        "-keyout", f"{d}/ca.key", "-out", f"{d}/ca.crt", "-subj", "/CN=ocubt-test-ca")
    run("openssl", "req", "-newkey", "rsa:2048", "-nodes",
        "-keyout", f"{d}/server.key", "-out", f"{d}/server.csr", "-subj", "/CN=ocubt-server")
    with open(f"{d}/ext", "w") as f:
        f.write("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\n"
                "extendedKeyUsage=serverAuth\n")
    run("openssl", "x509", "-req", "-in", f"{d}/server.csr", "-CA", f"{d}/ca.crt",
        "-CAkey", f"{d}/ca.key", "-CAcreateserial", "-days", "3650",
        "-extfile", f"{d}/ext", "-out", f"{d}/server.crt")


def ip4_udp(src, dst, sport, dport, payload):
    udp = struct.pack("!HHHH", sport, dport, 8 + len(payload), 0) + payload
    total = 20 + len(udp)
    hdr = struct.pack("!BBHHHBBH4s4s", 0x45, 0, total, 0x1111, 0, 64, 17, 0,
                      socket.inet_aton(src), socket.inet_aton(dst))
    s = sum(struct.unpack("!10H", hdr))
    while s >> 16:
        s = (s & 0xFFFF) + (s >> 16)
    hdr = hdr[:10] + struct.pack("!H", (~s) & 0xFFFF) + hdr[12:]
    return hdr + udp


def main():
    if not os.path.exists(OPENVPN):
        print(f"missing {OPENVPN}; run packaging/openvpn/build.sh first")
        return 1
    d = tempfile.mkdtemp(prefix="ocubt-fdtest-")
    make_pki(d)

    with open(f"{d}/auth.sh", "w") as f:
        f.write("#!/bin/sh\nexit 0\n")   # accept any user/password
    os.chmod(f"{d}/auth.sh", 0o755)
    with open(f"{d}/creds", "w") as f:
        f.write("testuser\ntestpass\n")
    os.chmod(f"{d}/creds", 0o600)
    # Hand the client exactly the address we choose, the way ocbridge will push
    # the Cisco-assigned INTERNAL_IP4_ADDRESS.
    os.mkdir(f"{d}/ccd")
    with open(f"{d}/ccd/DEFAULT", "w") as f:
        f.write(f"ifconfig-push {CLIENT_IP} 255.255.255.0\n")

    srv_ours, srv_theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
    cli_ours, cli_theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)

    server_conf = f"""
dev tun
dev-node fd:{srv_theirs.fileno()}
ifconfig-noexec
route-noexec
mode server
tls-server
topology subnet
server 10.99.0.0 255.255.255.0
client-config-dir {d}/ccd
proto udp
local 127.0.0.1
port {PORT}
ca {d}/ca.crt
cert {d}/server.crt
key {d}/server.key
dh none
verify-client-cert none
username-as-common-name
auth-user-pass-verify {d}/auth.sh via-file
script-security 2
keepalive 5 30
verb 3
"""
    client_conf = f"""
dev tun
dev-node fd:{cli_theirs.fileno()}
ifconfig-noexec
route-noexec
client
proto udp
remote 127.0.0.1 {PORT}
ca {d}/ca.crt
auth-user-pass {d}/creds
pull
verb 3
"""
    open(f"{d}/server.conf", "w").write(server_conf)
    open(f"{d}/client.conf", "w").write(client_conf)

    os.set_inheritable(srv_theirs.fileno(), True)
    os.set_inheritable(cli_theirs.fileno(), True)
    slog, clog = open(f"{d}/server.log", "wb"), open(f"{d}/client.log", "wb")
    srv = subprocess.Popen([OPENVPN, "--config", f"{d}/server.conf"],
                           stdout=slog, stderr=subprocess.STDOUT,
                           pass_fds=(srv_theirs.fileno(),))
    time.sleep(1.5)
    cli = subprocess.Popen([OPENVPN, "--config", f"{d}/client.conf"],
                           stdout=clog, stderr=subprocess.STDOUT,
                           pass_fds=(cli_theirs.fileno(),))

    try:
        # Wait for the client's Initialization Sequence Completed.
        up = False
        for _ in range(60):
            time.sleep(0.5)
            if os.path.exists(f"{d}/client.log"):
                t = open(f"{d}/client.log", errors="replace").read()
                if "Initialization Sequence Completed" in t:
                    up = True
                    break
                if cli.poll() is not None:
                    break
        check("tunnel comes up with both ends on inherited fds", up)

        ctext = open(f"{d}/client.log", errors="replace").read()
        check("client adopted the inherited descriptor", "adopted inherited descriptor" in ctext)
        check(f"client was pushed {CLIENT_IP} (no NAT needed)",
              f"ifconfig {CLIENT_IP}" in ctext or CLIENT_IP in ctext,
              [l for l in ctext.splitlines() if "PUSH: Received" in l][:1])

        if up:
            pkt = ip4_udp(CLIENT_IP, SERVER_IP, 4444, 5555, b"ocbridge-datapath")
            cli_ours.send(pkt)
            srv_ours.settimeout(5)
            got = b""
            try:
                got = srv_ours.recv(65535)
            except socket.timeout:
                pass
            check("packet injected at client fd arrives at server fd",
                  got == pkt, f"{len(got)}B, exact={got == pkt}")
    finally:
        for p in (cli, srv):
            p.terminate()
            try:
                p.wait(5)
            except subprocess.TimeoutExpired:
                p.kill()
        print(f"\nlogs: {d}/server.log {d}/client.log")

    print(f"\n-- {3 + (1 if fails == [] else 0)} checks, {len(fails)} failed --")
    return 1 if fails else 0


sys.exit(main())
