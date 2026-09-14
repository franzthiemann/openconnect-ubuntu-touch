#!/usr/bin/env python3
"""Phase 0.2 probe: what does openconnect --script-tun actually hand us?

openconnect execs this via /bin/sh -c, having exported the vpnc-script
environment plus VPNFD (its end of an AF_UNIX SOCK_DGRAM socketpair). We dump
the environment, then inject an ICMP echo request sourced from
INTERNAL_IP4_ADDRESS and wait for the reply -- which proves, in one shot, the
framing, that the gateway accepts packets from us, and that ocbridge's whole
data path is viable with no tun device and no privileges.
"""
import os
import socket
import struct
import sys
import time

OUT = os.environ.get("PROBE_OUT", "/tmp/scripttun_probe.txt")
log = open(OUT, "w")


def say(*a):
    print(*a, file=log, flush=True)
    print(*a, file=sys.stderr, flush=True)


def checksum(data: bytes) -> int:
    if len(data) % 2:
        data += b"\0"
    s = sum(struct.unpack("!%dH" % (len(data) // 2), data))
    while s >> 16:
        s = (s & 0xFFFF) + (s >> 16)
    return (~s) & 0xFFFF


def ip4(src, dst, proto, payload, ident=0x4242):
    total = 20 + len(payload)
    hdr = struct.pack("!BBHHHBBH4s4s", 0x45, 0, total, ident, 0, 64, proto, 0,
                      socket.inet_aton(src), socket.inet_aton(dst))
    hdr = hdr[:10] + struct.pack("!H", checksum(hdr)) + hdr[12:]
    return hdr + payload


def icmp_echo(ident, seq, payload=b"openconnect_ubt probe"):
    body = struct.pack("!BBHHH", 8, 0, 0, ident, seq) + payload
    return body[:2] + struct.pack("!H", checksum(body)) + body[4:]


say("=== script-tun environment ===")
interesting = ("VPNFD", "VPNGATEWAY", "VPNPID", "reason", "INTERNAL_IP4_",
               "INTERNAL_IP6_", "CISCO_")
for k in sorted(os.environ):
    if any(k.startswith(p) or k == p for p in interesting):
        say(f"  {k}={os.environ[k]}")

fd = int(os.environ["VPNFD"])
sock = socket.fromfd(fd, socket.AF_UNIX, socket.SOCK_DGRAM)
say(f"\n=== VPNFD={fd} socket family={sock.family.name} type={sock.type.name} ===")

my_ip = os.environ["INTERNAL_IP4_ADDRESS"]
# ocserv's tunnel-side address is the .1 of the pool; derive it generically.
o = my_ip.split(".")
peer = os.environ.get("PROBE_PEER", ".".join(o[:3] + ["1"]))
say(f"probing {my_ip} -> {peer}")

pkt = ip4(my_ip, peer, 1, icmp_echo(0x1234, 1))
sock.send(pkt)
say(f"sent {len(pkt)} bytes (IP total_len={struct.unpack('!H', pkt[2:4])[0]})")

sock.settimeout(5.0)
deadline = time.time() + 5.0
seen = 0
while time.time() < deadline:
    try:
        data = sock.recv(65535)
    except socket.timeout:
        break
    seen += 1
    ver = data[0] >> 4
    ihl = (data[0] & 0xF) * 4
    total_len = struct.unpack("!H", data[2:4])[0]
    proto = data[9]
    src = socket.inet_ntoa(data[12:16])
    dst = socket.inet_ntoa(data[16:20])
    framed = "EXACT" if total_len == len(data) else f"MISMATCH(len={len(data)})"
    say(f"  rx #{seen}: {len(data)}B ipver={ver} ihl={ihl} proto={proto} "
        f"{src}->{dst} total_len={total_len} framing={framed}")
    if proto == 1 and data[ihl] == 0:
        say("  *** ICMP ECHO REPLY -- userspace data path works ***")
        break

if not seen:
    say("  (no packets received)")
say("\n=== done ===")
log.close()
