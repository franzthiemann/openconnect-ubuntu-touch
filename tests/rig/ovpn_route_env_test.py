#!/usr/bin/env python3
"""Phase 0.5/0.7: what does an NM-like client actually get in its --up env?

NetworkManager-openvpn always runs openvpn with --route-noexec --ifconfig-noexec
and recovers the routing from the --up environment (route_network_N /
route_netmask_N / route_gateway_N, and foreign_option_N for DNS). This test
reproduces that exactly, with a stock *unpatched* client on `--dev null` so it
needs no tun and no root, and asserts on the environment openvpn exports.

The load-bearing question: does `push "route X Y net_gateway"` -- our fix for
the routing loop -- still resolve to the real default gateway when route
execution is disabled?
"""
import os
import re
import socket
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
OPENVPN = os.path.abspath(os.path.join(HERE, "..", "..", "packaging", "openvpn", "out", "openvpn"))
PORT = 11195
BYPASS_IP = "203.0.113.77"          # stands in for the Cisco gateway's real IP
fails = []


def check(label, ok, detail=""):
    print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f" -- {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def run(*a):
    return subprocess.run(a, check=True, capture_output=True)


def host_default_gw():
    out = subprocess.run(["ip", "route", "show", "default"],
                         capture_output=True, text=True).stdout
    m = re.search(r"default via (\S+)", out)
    return m.group(1) if m else None


def main():
    gw = host_default_gw()
    if not os.path.exists(OPENVPN):
        print("build packaging/openvpn/out/openvpn first")
        return 1
    d = tempfile.mkdtemp(prefix="ocubt-routeenv-")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "3650",
        "-keyout", f"{d}/ca.key", "-out", f"{d}/ca.crt", "-subj", "/CN=ocubt-test-ca")
    run("openssl", "req", "-newkey", "rsa:2048", "-nodes",
        "-keyout", f"{d}/server.key", "-out", f"{d}/server.csr", "-subj", "/CN=ocubt-server")
    open(f"{d}/ext", "w").write("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,"
                                "keyEncipherment\nextendedKeyUsage=serverAuth\n")
    run("openssl", "x509", "-req", "-in", f"{d}/server.csr", "-CA", f"{d}/ca.crt",
        "-CAkey", f"{d}/ca.key", "-CAcreateserial", "-days", "3650",
        "-extfile", f"{d}/ext", "-out", f"{d}/server.crt")
    open(f"{d}/auth.sh", "w").write("#!/bin/sh\nexit 0\n")
    os.chmod(f"{d}/auth.sh", 0o755)
    open(f"{d}/creds", "w").write("testuser\ntestpass\n")
    os.chmod(f"{d}/creds", 0o600)
    # Dump the whole --up environment the way NM's helper reads it.
    open(f"{d}/up.sh", "w").write(f"#!/bin/sh\nenv > {d}/up.env\nexit 0\n")
    os.chmod(f"{d}/up.sh", 0o755)

    ours, theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
    open(f"{d}/server.conf", "w").write(f"""
dev tun
dev-node fd:{theirs.fileno()}
ifconfig-noexec
route-noexec
mode server
tls-server
topology subnet
server 10.99.0.0 255.255.255.0
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
push "route {BYPASS_IP} 255.255.255.255 net_gateway"
push "route 10.200.0.0 255.255.0.0"
push "dhcp-option DNS 10.99.0.53"
push "dhcp-option DOMAIN rig.test"
push "redirect-gateway def1"
verb 3
""")
    # --dev null: a stock client with no tun and no privileges, otherwise
    # configured exactly as NetworkManager-openvpn configures one.
    open(f"{d}/client.conf", "w").write(f"""
dev null
ifconfig-noexec
route-noexec
client
proto udp
remote 127.0.0.1 {PORT}
ca {d}/ca.crt
auth-user-pass {d}/creds
pull
script-security 2
up {d}/up.sh
verb 3
""")
    os.set_inheritable(theirs.fileno(), True)
    slog = open(f"{d}/server.log", "wb")
    clog = open(f"{d}/client.log", "wb")
    srv = subprocess.Popen([OPENVPN, "--config", f"{d}/server.conf"],
                           stdout=slog, stderr=subprocess.STDOUT,
                           pass_fds=(theirs.fileno(),))
    time.sleep(1.5)
    cli = subprocess.Popen([OPENVPN, "--config", f"{d}/client.conf"],
                           stdout=clog, stderr=subprocess.STDOUT)
    try:
        for _ in range(60):
            time.sleep(0.5)
            if os.path.exists(f"{d}/up.env"):
                break
        env = {}
        if os.path.exists(f"{d}/up.env"):
            for line in open(f"{d}/up.env", errors="replace"):
                if "=" in line:
                    k, v = line.rstrip("\n").split("=", 1)
                    env[k] = v
        check("--up script ran (client reached the routing stage)", bool(env))

        routes = {k: v for k, v in env.items() if k.startswith("route_")}
        print("    route_* environment:")
        for k in sorted(routes):
            print(f"      {k}={routes[k]}")

        check("net_gateway resolved to the host's real default gateway",
              gw is not None and routes.get("route_gateway_1") == gw,
              f"route_gateway_1={routes.get('route_gateway_1')} host gw={gw}")
        check("the bypass host route reached the client",
              routes.get("route_network_1") == BYPASS_IP
              and routes.get("route_netmask_1") == "255.255.255.255")
        check("the split route reached the client",
              any(v == "10.200.0.0" for k, v in routes.items() if k.startswith("route_network_")))
        fo = {k: v for k, v in env.items() if k.startswith("foreign_option_")}
        check("pushed DNS arrived as foreign_option_*",
              any("DNS 10.99.0.53" in v for v in fo.values()), str(list(fo.values())))
        check("redirect-gateway produced NO /1 routes (it is a no-op under route-noexec)",
              not any(v in ("0.0.0.0", "128.0.0.0") for k, v in routes.items()
                      if k.startswith("route_network_")))
    finally:
        for p in (cli, srv):
            p.terminate()
            try:
                p.wait(5)
            except subprocess.TimeoutExpired:
                p.kill()
        print(f"\nlogs: {d}")
    print(f"\n-- {len(fails)} failed --")
    return 1 if fails else 0


sys.exit(main())
