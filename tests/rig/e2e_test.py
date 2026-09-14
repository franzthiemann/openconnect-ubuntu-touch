#!/usr/bin/env python3
"""Phase 0.8: the whole architecture, end to end, with no root anywhere.

    ocserv (Docker)  <--AnyConnect--  openconnect --script-tun
                                          |
                                       ocbridge
                                          |
                                    patched openvpn (server, fd tun, loopback)
                                          ^
                                    patched openvpn (client, fd tun)
                                          |
                                   this script injects an ICMP echo

The client stands in for Ubuntu Touch's NetworkManager OpenVPN client. It runs
on an inherited descriptor purely so the test needs no tun device and no root;
on the device that role is played by NM, which does have both. Everything
between openconnect and the OpenVPN server is byte-for-byte what ships.

Passing this means a packet originating "on the phone" reaches the VPN gateway
and the reply comes back.
"""
import json
import os
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
# Point this at a click's installed bin directory to test exactly what ships:
#   OCUBT_BIN_DIR=build/x86_64-linux-gnu/app/install/lib/x86_64-linux-gnu/bin
STAGE = os.environ.get("OCUBT_BIN_DIR") or os.path.join(
    ROOT, "build", "x86_64-linux-gnu", "app", "install", "lib", "x86_64-linux-gnu", "bin")
OPENVPN = os.path.join(STAGE, "openvpn")
OCBRIDGE = os.path.join(STAGE, "ocbridge")
# Use the bundled openconnect when there is one, so the test exercises our own
# minimal build rather than whatever the host happens to have installed.
OPENCONNECT = os.path.join(STAGE, "openconnect")
if not os.path.exists(OPENCONNECT):
    OPENCONNECT = "openconnect"
RIG = os.path.join(HERE, "rig.sh")
PORT = 11194
PEER = "10.99.0.1"          # ocserv's address inside the tunnel
fails = []


def check(label, ok, detail=""):
    print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f" -- {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def checksum(data):
    if len(data) % 2:
        data += b"\0"
    s = sum(struct.unpack("!%dH" % (len(data) // 2), data))
    while s >> 16:
        s = (s & 0xFFFF) + (s >> 16)
    return (~s) & 0xFFFF


def icmp_echo_packet(src, dst):
    body = struct.pack("!BBHHH", 8, 0, 0, 0x4321, 1) + b"ocbridge-e2e"
    icmp = body[:2] + struct.pack("!H", checksum(body)) + body[4:]
    total = 20 + len(icmp)
    hdr = struct.pack("!BBHHHBBH4s4s", 0x45, 0, total, 0x7777, 0, 64, 1, 0,
                      socket.inet_aton(src), socket.inet_aton(dst))
    hdr = hdr[:10] + struct.pack("!H", checksum(hdr)) + hdr[12:]
    return hdr + icmp


def wait_for(predicate, timeout, interval=0.25):
    deadline = time.time() + timeout
    while time.time() < deadline:
        v = predicate()
        if v:
            return v
        time.sleep(interval)
    return None


def main():
    print(f"using binaries from {STAGE}")
    for p in (OPENVPN, OCBRIDGE):
        if not os.path.exists(p):
            print(f"missing {p}; run 'clickable build --arch amd64' first")
            return 1

    manage_rig = os.environ.get("OCUBT_SKIP_RIG") != "1"
    if manage_rig:
        subprocess.run([RIG, "up"], check=True, capture_output=True)

    # Scrape the pin openconnect prints when it cannot verify the self-signed
    # certificate -- the same TOFU path the app uses on a first connection.
    probe = subprocess.run(
        [OPENCONNECT, "--protocol=anyconnect", "--authenticate", "--non-inter",
         "https://localhost:4443"],
        capture_output=True, text=True)
    fp = ""
    for line in (probe.stdout + probe.stderr).splitlines():
        if "--servercert" in line:
            fp = line.split("--servercert", 1)[1].strip()
            break
    if not fp:
        print("could not obtain the rig's certificate fingerprint")
        print((probe.stdout + probe.stderr)[-1500:])
        return 1

    d = tempfile.mkdtemp(prefix="ocubt-e2e-")
    data_dir, run_dir = os.path.join(d, "data"), os.path.join(d, "data", "run")
    env = dict(os.environ,
               OCBRIDGE_DATA_DIR=data_dir,
               OCBRIDGE_PORT=str(PORT),
               XDG_RUNTIME_DIR="")            # force the data-dir fallback
    oc_log = open(os.path.join(d, "openconnect.log"), "wb")

    oc = subprocess.Popen(
        [OPENCONNECT, "--protocol=anyconnect", "--user=testuser",
         "--passwd-on-stdin", "--non-inter", f"--servercert={fp}",
         "--useragent=AnyConnect-compatible OpenConnect VPN Agent v9.12",
         "--script-tun", "--script", OCBRIDGE, "https://localhost:4443"],
        stdin=subprocess.PIPE, stdout=oc_log, stderr=subprocess.STDOUT, env=env)
    oc.stdin.write(b"testpass\n")
    oc.stdin.flush()
    oc.stdin.close()

    cli = None
    try:
        status_path = os.path.join(run_dir, "status.json")

        def read_status():
            try:
                with open(status_path) as f:
                    s = json.load(f)
                return s if s.get("state") == "up" else None
            except Exception:
                return None

        st = wait_for(read_status, 45)
        check("ocbridge came up and published status", st is not None)
        if st is None:
            print(open(os.path.join(d, "openconnect.log"), errors="replace").read()[-3000:])
            return 1

        print(f"    assigned {st['address']}  mtu {st['mtu']}  "
              f"routes {st['routes']}  dns {st['dns']}")
        check("status carries the credentials the user must type into Settings",
              bool(st["vpn_user"]) and bool(st["vpn_pass"]) and os.path.exists(st["ca_cert"]))
        check("split routes from the gateway reached ocbridge", len(st["routes"]) == 2)
        check("status names the transport the client must use",
              st.get("proto") in ("tcp", "udp"), st.get("proto"))
        check("DNS from the gateway reached ocbridge", st["dns"] == ["10.99.0.53"])

        # Now the NetworkManager stand-in.
        creds = os.path.join(d, "creds")
        with open(creds, "w") as f:
            f.write(f"{st['vpn_user']}\n{st['vpn_pass']}\n")
        os.chmod(creds, 0o600)

        ours, theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
        os.set_inheritable(theirs.fileno(), True)
        client_conf = os.path.join(d, "client.conf")
        # Follow the server's transport, exactly as the real client must: a
        # mismatch is invisible server-side and shows up only as the client
        # getting "Connection refused" on a port that is plainly open.
        client_proto = "tcp-client" if st.get("proto", "tcp") == "tcp" else "udp"
        with open(client_conf, "w") as f:
            f.write(f"""
dev tun
dev-node fd:{theirs.fileno()}
ifconfig-noexec
route-noexec
client
proto {client_proto}
remote 127.0.0.1 {PORT}
ca {st['ca_cert']}
auth-user-pass {creds}
pull
verb 3
""")
        clog_path = os.path.join(d, "client.log")
        clog = open(clog_path, "wb")
        cli = subprocess.Popen([OPENVPN, "--config", client_conf],
                               stdout=clog, stderr=subprocess.STDOUT,
                               pass_fds=(theirs.fileno(),))

        def client_up():
            try:
                t = open(clog_path, errors="replace").read()
            except Exception:
                return None
            return t if "Initialization Sequence Completed" in t else None

        ctext = wait_for(client_up, 45)
        check("the OpenVPN client authenticated and came up", ctext is not None)
        if ctext:
            check(f"client was assigned the gateway address {st['address']} (no NAT)",
                  f"ifconfig {st['address']} " in ctext or f",ifconfig {st['address']}," in ctext,
                  [l.split("PUSH: Received control message: ")[-1]
                   for l in ctext.splitlines() if "PUSH: Received" in l][:1])

            # The payoff: a packet that starts at the "phone" end must reach the
            # VPN gateway and come back.
            ours.send(icmp_echo_packet(st["address"], PEER))
            ours.settimeout(8)
            reply = b""
            try:
                while True:
                    data = ours.recv(65535)
                    if len(data) >= 20 and data[9] == 1 and data[20] == 0:
                        reply = data
                        break
            except socket.timeout:
                pass
            check("ICMP echo crossed the whole chain and the reply came back",
                  bool(reply),
                  f"{socket.inet_ntoa(reply[12:16])} -> {socket.inet_ntoa(reply[16:20])}"
                  if reply else "no reply")

            # Counters are republished on a timer, so poll rather than
            # sampling once and racing the tick.
            def counted():
                s = json.load(open(status_path))
                return s if s["bytes_in"] > 0 and s["bytes_out"] > 0 else None

            final = wait_for(counted, 10) or json.load(open(status_path))
            check("ocbridge counted traffic in both directions",
                  final["bytes_in"] > 0 and final["bytes_out"] > 0,
                  f"in={final['bytes_in']} out={final['bytes_out']}")
            check("ocbridge saw the client connect", final["client_up"])

        # Teardown: openconnect SIGHUPs the script's process group.
        oc.send_signal(signal.SIGINT)
        gone = wait_for(lambda: oc.poll() is not None, 15)
        check("openconnect shut down cleanly", gone is not None)
        down = wait_for(lambda: json.load(open(status_path)).get("state") in ("down", "error"), 10)
        check("ocbridge published a terminal state and exited",
              down is not None, json.load(open(status_path)).get("state"))
    finally:
        for p in (cli, oc):
            if p and p.poll() is None:
                p.kill()
        if manage_rig:
            subprocess.run([RIG, "down"], capture_output=True)
        print(f"\nlogs: {d}")

    print(f"\n-- {len(fails)} failed --")
    return 1 if fails else 0


sys.exit(main())
