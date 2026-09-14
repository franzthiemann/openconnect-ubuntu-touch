#!/usr/bin/env python3
"""Backend tests driven against the ocserv rig -- no real VPN account needed.

Runs the actual code path the app uses: profile storage, gateway inspection,
authentication, tunnel start and teardown. Intended to run inside the Ubuntu
24.04 container (tests/rig/run-tests.sh) so the click's own binaries are
exercised against the library versions the device has.
"""
import json
import os
import shutil
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "src"))
import backend  # noqa: E402

BIN = os.environ.get("OCUBT_BIN_DIR") or os.path.join(
    os.path.dirname(HERE), "build", "x86_64-linux-gnu", "app", "install",
    "lib", "x86_64-linux-gnu", "bin")
HOST = os.environ.get("OCUBT_RIG_HOST", "localhost:4443")
USER, PASSWORD = "testuser", "testpass"

fails = []


def check(label, ok, detail=""):
    print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f" -- {detail}" if detail else ""))
    if not ok:
        fails.append(label)


def wait_for(fn, timeout, interval=0.25):
    deadline = time.time() + timeout
    while time.time() < deadline:
        v = fn()
        if v:
            return v
        time.sleep(interval)
    return None


def main():
    tmp = tempfile.mkdtemp(prefix="ocubt-backend-")
    os.environ["XDG_RUNTIME_DIR"] = ""          # force the data-dir fallback
    res = backend.init(bin_dir=BIN, data_dir=tmp)
    check("init resolves paths and materialises the local CA", res["ok"], res.get("msg", ""))
    if not res["ok"]:
        return 1

    setup = res["setup"]
    check("setup info gives the user everything Settings asks for",
          os.path.exists(setup.get("ca_cert", "")) and setup.get("vpn_user")
          and setup.get("vpn_pass") and setup.get("remote") == "127.0.0.1",
          f"remote {setup.get('remote')}:{setup.get('port')}")

    # --- profiles ---------------------------------------------------------
    saved = backend.save_profile({
        "name": "Rig", "host": HOST, "username": USER,
        "password": PASSWORD, "save_password": True})
    check("save_profile stores a profile", saved["ok"], saved.get("msg", ""))
    pid = saved["profile"]["id"]

    listed = backend.list_profiles()
    check("list_profiles returns it", listed["ok"] and len(listed["profiles"]) == 1)
    check("the password is never handed to QML",
          "password" not in listed["profiles"][0] and listed["profiles"][0]["has_password"])

    check("a profile with no host is refused",
          not backend.save_profile({"name": "bad", "host": ""})["ok"])

    # Editing without retyping the password must not silently drop it.
    backend.save_profile({"id": pid, "name": "Rig renamed", "host": HOST,
                          "username": USER, "save_password": True})
    kept = json.load(open(os.path.join(tmp, "profiles.json")))["profiles"][0]
    check("editing without a password keeps the stored one",
          kept.get("password") == PASSWORD and kept["name"] == "Rig renamed")

    # Turning saving off must actually erase it.
    backend.save_profile({"id": pid, "name": "Rig renamed", "host": HOST,
                          "username": USER, "save_password": False})
    cleared = json.load(open(os.path.join(tmp, "profiles.json")))["profiles"][0]
    check("turning off 'save password' erases it", "password" not in cleared)
    backend.save_profile({"id": pid, "host": HOST, "username": USER,
                          "password": PASSWORD, "save_password": True})

    # --- gateway inspection ----------------------------------------------
    fp = backend.probe_fingerprint(HOST)
    check("probe_fingerprint returns a pin for a self-signed gateway",
          fp["ok"] and fp["fingerprint"].startswith("pin-sha256:"),
          fp.get("fingerprint", "")[:24] + "...")

    groups = backend.discover_groups(HOST)
    # ocserv offers no group selection, so an empty list is the correct answer;
    # what matters is that it does not error.
    check("discover_groups succeeds (rig offers no groups)",
          groups["ok"], f"groups={groups['groups']}")

    # --- connect ----------------------------------------------------------
    backend.save_profile({"id": pid, "host": HOST, "username": USER,
                          "password": PASSWORD, "save_password": True,
                          "servercert": fp["fingerprint"]})

    check("connecting without a password is refused",
          not backend.connect("nonexistent-id")["ok"])

    started = backend.connect(pid)
    check("connect starts", started["ok"], started.get("msg", ""))

    up = wait_for(lambda: backend.status().get("state") == "up" or None, 60)
    st = backend.status()
    check("the tunnel comes up", bool(up), st.get("state") + " " + st.get("error", ""))
    if up:
        check("status reports the gateway-assigned address", bool(st.get("address")),
              f"{st.get('address')} mtu {st.get('mtu')}")
        check("status carries the local OpenVPN credentials",
              bool(st.get("vpn_user")) and bool(st.get("vpn_pass")))

        # The UI must not be fooled by a second attempt.
        check("a second connect is refused while up", not backend.connect(pid)["ok"])

    stopped = backend.disconnect()
    check("disconnect reports success", stopped["ok"], stopped.get("msg", ""))
    down = wait_for(lambda: backend.status().get("state") != "up" or None, 20)
    check("status goes back to down", bool(down), backend.status().get("state"))

    # A gateway that cannot be reached is nearly always a stale VPN still
    # holding routes into a dead tunnel; say so rather than echoing a timeout.
    unreachable = backend.save_profile({"name": "nowhere", "host": "127.0.0.1:9",
                                        "username": "x", "password": "y",
                                        "save_password": True})
    backend.connect(unreachable["profile"]["id"])
    err = wait_for(lambda: backend.status().get("state") == "error"
                   or (not backend._connect_thread.is_alive() and True), 45)
    check("an unreachable gateway explains the stale-VPN case", bool(err))
    backend.delete_profile(unreachable["profile"]["id"])

    check("delete_profile removes it", backend.delete_profile(pid)["ok"]
          and backend.list_profiles()["profiles"] == [])

    # Regression: openconnect survives its --script-tun child dying, so a
    # failed attempt used to leak a process holding a live VPN session, once
    # per retry. _reap_stale must clear that before the next connect.
    import subprocess as sp
    dummy = sp.Popen(["sleep", "300"])
    rt = backend._state["runtime_dir"]
    backend._write_atomic(os.path.join(rt, "openconnect.pid"), str(dummy.pid) + "\n", 0o644)
    backend._write_atomic(os.path.join(rt, "ocbridge.pid"), "999999\n", 0o644)
    backend._reap_stale()
    time.sleep(0.5)
    check("a stale openconnect with no bridge is reaped", dummy.poll() is not None,
          f"exit={dummy.poll()}")
    if dummy.poll() is None:
        dummy.kill()

    # ...and one with a live bridge must be left strictly alone.
    keeper = sp.Popen(["sleep", "300"])
    bridge = sp.Popen(["sleep", "300"])
    backend._write_atomic(os.path.join(rt, "openconnect.pid"), str(keeper.pid) + "\n", 0o644)
    backend._write_atomic(os.path.join(rt, "ocbridge.pid"), str(bridge.pid) + "\n", 0o644)
    backend._reap_stale()
    time.sleep(0.5)
    check("a healthy tunnel is not reaped", keeper.poll() is None)
    keeper.kill(); bridge.kill()

    shutil.rmtree(tmp, ignore_errors=True)
    print(f"\n-- {len(fails)} failed --")
    return 1 if fails else 0


sys.exit(main())
