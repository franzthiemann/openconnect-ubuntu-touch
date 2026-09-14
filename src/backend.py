"""PyOtherSide backend for the OpenConnect VPN click.

Control plane only: it authenticates to the gateway, starts and stops
openconnect, stores profiles, and reads the status ocbridge publishes. The data
plane -- moving packets between the Cisco tunnel and the local OpenVPN server --
lives entirely in ocbridge, so nothing performance-sensitive happens here.

Every entry point returns a plain dict and never raises across the PyOtherSide
boundary; an exception there surfaces in QML as an unhelpful generic error.
"""

import json
import os
import re
import signal
import subprocess
import threading
import time
import uuid

try:
    import pyotherside
except ImportError:  # running on the host, in tests
    pyotherside = None

PKG = "ocvpn.franzthiemann"

# openconnect 9.12's default User-Agent is "Open AnyConnect VPN Agent v9.12",
# which does not begin with "AnyConnect". Cisco heads answer HTTP 404 to the
# XML-POST auth endpoint for such clients and openconnect then silently falls
# back to a legacy path that administrators often have not tested. Verified
# against a real gateway: without this, every login takes the fallback route.
USER_AGENT = "AnyConnect-compatible OpenConnect VPN Agent v9.12"

PROTOCOL = "anyconnect"
AUTH_TIMEOUT = 60

_state = {
    "bin_dir": None,
    "data_dir": None,
    "runtime_dir": None,
}
_connect_thread = None


# --------------------------------------------------------------------- paths


def _default_data_dir():
    base = os.environ.get(
        "XDG_DATA_HOME", os.path.join(os.path.expanduser("~"), ".local", "share")
    )
    return os.path.join(base, PKG)


def _default_runtime_dir(data_dir):
    """Mirror ocbridge's RuntimeDir so both agree on where status.json lives.

    A confined app sees XDG_RUNTIME_DIR=/run/user/<uid>, and the generated
    AppArmor profile grants ``/run/user/*/<package>/**``, so appending the
    package name is correct. See ocbridge's RuntimeDir for the detail and for
    why the basename check is there.
    """
    d = os.environ.get("XDG_RUNTIME_DIR", "")
    if not d:
        return os.path.join(data_dir, "run")
    if os.path.basename(d.rstrip("/")) == PKG:
        return d
    return os.path.join(d, PKG)


def _tool(name):
    return os.path.join(_state["bin_dir"] or "", name)


def _profiles_path():
    return os.path.join(_state["data_dir"], "profiles.json")


def _emit(event, payload):
    if pyotherside is not None:
        pyotherside.send(event, payload)


def _write_atomic(path, text, mode=0o600):
    """Write via a temporary file so a crash cannot leave a half-written file."""
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    try:
        with os.fdopen(fd, "w") as fh:
            fh.write(text)
    except Exception:
        os.unlink(tmp)
        raise
    os.replace(tmp, path)


# ---------------------------------------------------------------------- init


def init(bin_dir=None, data_dir=None):
    """Resolve paths and make sure the local CA and credentials exist."""
    try:
        _state["bin_dir"] = bin_dir or os.environ.get("OCVPN_BIN_DIR") or ""
        _state["data_dir"] = data_dir or _default_data_dir()
        _state["runtime_dir"] = _default_runtime_dir(_state["data_dir"])
        os.makedirs(_state["data_dir"], mode=0o700, exist_ok=True)
        os.makedirs(_state["runtime_dir"], mode=0o700, exist_ok=True)
        setup = setup_info()
        result = {
            "ok": True,
            "bin_dir": _state["bin_dir"],
            "data_dir": _state["data_dir"],
            "setup": setup if setup.get("ok") else {},
        }
        if not setup.get("ok"):
            # Do not swallow this. Without the CA and credentials the user
            # cannot create the Settings profile, so the app is unusable and
            # the reason belongs in front of them.
            result["setup_error"] = setup.get("msg", "unknown error")
        return result
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


def setup_info():
    """What the user must reproduce by hand in the Settings VPN editor.

    A confined app cannot create the NetworkManager connection itself -- the
    `networking` policy group denies NM's D-Bus API outright -- so the app can
    only show the values and let the user type them in once.
    """
    try:
        out = subprocess.run(
            [_tool("ocbridge"), "-init", "-data-dir", _state["data_dir"]],
            capture_output=True, text=True, timeout=60,
            env=dict(os.environ, OCBRIDGE_PKG=PKG),
        )
        if out.returncode != 0:
            return {"ok": False, "msg": (out.stderr or out.stdout).strip()}
        return {"ok": True, **json.loads(out.stdout)}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


# ------------------------------------------------------------------ profiles


def list_profiles():
    """Return saved profiles. Passwords are never handed to QML."""
    try:
        return {"ok": True, "profiles": [_redact(p) for p in _load_profiles()]}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc), "profiles": []}


def _redact(profile):
    out = dict(profile)
    out["has_password"] = bool(out.pop("password", ""))
    return out


def _load_profiles():
    path = _profiles_path()
    if not os.path.exists(path):
        return []
    with open(path) as fh:
        data = json.load(fh)
    return data.get("profiles", [])


def _store_profiles(profiles):
    _write_atomic(_profiles_path(), json.dumps({"profiles": profiles}, indent=2))


def save_profile(profile):
    """Create or update a profile. Returns the stored (redacted) profile."""
    try:
        profiles = _load_profiles()
        pid = profile.get("id") or uuid.uuid4().hex[:12]
        existing = next((p for p in profiles if p["id"] == pid), None)

        stored = dict(existing or {})
        stored.update({
            "id": pid,
            "name": (profile.get("name") or profile.get("host") or "VPN").strip(),
            "host": (profile.get("host") or "").strip(),
            "username": (profile.get("username") or "").strip(),
            "authgroup": (profile.get("authgroup") or "").strip(),
            "save_password": bool(profile.get("save_password")),
            "servercert": (profile.get("servercert")
                           or (existing or {}).get("servercert") or "").strip(),
        })
        # An empty password field means "leave what is stored alone"; clearing a
        # saved password is done by turning save_password off.
        if profile.get("password"):
            stored["password"] = profile["password"]
        if not stored["save_password"]:
            stored.pop("password", None)

        if not stored["host"]:
            return {"ok": False, "msg": "A gateway host is required."}

        if existing:
            profiles = [stored if p["id"] == pid else p for p in profiles]
        else:
            profiles.append(stored)
        _store_profiles(profiles)
        return {"ok": True, "profile": _redact(stored)}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


def delete_profile(profile_id):
    try:
        _store_profiles([p for p in _load_profiles() if p["id"] != profile_id])
        return {"ok": True}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


def _profile(profile_id):
    return next((p for p in _load_profiles() if p["id"] == profile_id), None)


# -------------------------------------------------------- gateway inspection


GROUP_RE = re.compile(r"^GROUP:\s*\[([^\]]*)\]")
SERVERCERT_RE = re.compile(r"--servercert\s+(\S+)")


def discover_groups(host):
    """Ask the gateway which authentication groups it offers.

    Needs no credentials: openconnect prints the group prompt before it asks
    for anything else, so a --non-inter run fails at exactly that point with
    the list on stderr. The group must be chosen before logging in, because
    --passwd-on-stdin has already consumed the one line of stdin by the time
    the prompt appears.
    """
    try:
        out = _run_openconnect(["--authenticate", "--non-inter", _url(host)], "")
        text = out.stdout + out.stderr
        for line in text.splitlines():
            m = GROUP_RE.match(line.strip())
            if m:
                return {"ok": True, "groups": [g for g in m.group(1).split("|") if g]}
        return {"ok": True, "groups": [], "msg": _first_error(text)}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc), "groups": []}


def probe_fingerprint(host):
    """Trust-on-first-use: get the pin openconnect suggests for this gateway.

    With --non-inter and no --servercert, a verification failure prints
    'To trust this server in future, perhaps add this to your command line:
    --servercert pin-sha256:...' on stderr and exits. A gateway with a
    publicly-trusted certificate prints nothing, which is also fine: there is
    then nothing to pin and normal verification applies.
    """
    try:
        out = _run_openconnect(["--authenticate", "--non-inter", _url(host)], "")
        m = SERVERCERT_RE.search(out.stdout + out.stderr)
        return {"ok": True, "fingerprint": m.group(1) if m else ""}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc), "fingerprint": ""}


def _url(host):
    host = (host or "").strip()
    if host.startswith("http://") or host.startswith("https://"):
        return host
    return "https://" + host


def _run_openconnect(args, stdin_text, timeout=AUTH_TIMEOUT):
    return subprocess.run(
        [_tool("openconnect"), "--protocol=" + PROTOCOL, "--useragent=" + USER_AGENT] + args,
        input=stdin_text, capture_output=True, text=True, timeout=timeout,
    )


# A dead tunnel leaves NetworkManager holding routes that point at it,
# including the one to the gateway, so the next attempt cannot reach the
# gateway at all. The app cannot clear that -- it may not touch NetworkManager
# -- so it has to say so.
STALE_VPN_HINT = ("Could not reach the gateway. If the VPN is still switched on "
                  "in Settings, turn it off first: its routes still point into "
                  "the old tunnel, including the route to the gateway itself.")

_UNREACHABLE = ("Connection timed out", "Network is unreachable", "No route to host",
                "Failed to connect", "Connection refused", "Temporary failure")


def _first_error(text):
    """Pick the most useful line out of openconnect's output for the UI."""
    interesting = ("Failed to", "failed", "Error", "error", "Unable", "denied",
                   "Invalid", "refused", "timed out", "not known")
    lines = [l.strip() for l in text.splitlines() if l.strip()]
    for line in reversed(lines):
        if any(k in line for k in interesting):
            return line
    return lines[-1] if lines else "Unknown error"


# ----------------------------------------------------------------- connexion


def connect(profile_id, password="", extra_responses=None):
    """Authenticate, then start the tunnel. Runs on a thread; emits events.

    Events (via pyotherside.send):
      connect_state  {"phase": "authenticating"|"starting"|"up"|"error", ...}
    """
    global _connect_thread
    try:
        if _connect_thread and _connect_thread.is_alive():
            return {"ok": False, "msg": "A connection attempt is already running."}
        profile = _profile(profile_id)
        if not profile:
            return {"ok": False, "msg": "No such profile."}
        if status().get("state") == "up":
            return {"ok": False, "msg": "Already connected."}
        _reap_stale()

        pw = password or profile.get("password") or ""
        if not pw:
            return {"ok": False, "msg": "A password is required."}

        _connect_thread = threading.Thread(
            target=_connect_worker, args=(profile, pw, list(extra_responses or [])),
            daemon=True)
        _connect_thread.start()
        return {"ok": True}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


def _reap_stale():
    """Kill an openconnect left behind by a tunnel whose bridge never started.

    status() reports 'down' when ocbridge is gone, but openconnect itself
    survives its child's death, so without this a failed attempt leaks a
    process -- and a live VPN session -- for every retry.
    """
    pid = _read_pid("openconnect.pid")
    if pid and _alive(pid) and not _alive(_read_pid("ocbridge.pid")):
        _terminate(pid)


def _connect_worker(profile, password, extra_responses):
    try:
        _emit("connect_state", {"phase": "authenticating"})
        auth = _authenticate(profile, password, extra_responses)
        if not auth.get("ok"):
            _emit("connect_state", {"phase": "error", **auth})
            return

        _emit("connect_state", {"phase": "starting"})
        started = _start_tunnel(profile, auth)
        if not started.get("ok"):
            _emit("connect_state", {"phase": "error", **started})
            return
        _emit("connect_state", {"phase": "up"})
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        _emit("connect_state", {"phase": "error", "msg": str(exc)})


COOKIE_KEYS = ("COOKIE", "HOST", "CONNECT_URL", "FINGERPRINT", "RESOLVE")


def _authenticate(profile, password, extra_responses):
    """Run the unprivileged half: HTTPS login, yielding a session cookie."""
    args = ["--authenticate", "--non-inter", "--passwd-on-stdin"]
    if profile.get("username"):
        args.append("--user=" + profile["username"])
    if profile.get("authgroup"):
        args.append("--authgroup=" + profile["authgroup"])
    if profile.get("servercert"):
        args.append("--servercert=" + profile["servercert"])
    args.append(_url(profile["host"]))

    # openconnect consumes the first stdin line as the password, and because
    # --passwd-on-stdin also sets allow_stdin_read it keeps taking further
    # lines for any later prompt. That is how a second factor is answered, with
    # no change to this code beyond passing more responses.
    stdin_text = "\n".join([password] + list(extra_responses)) + "\n"
    out = _run_openconnect(args, stdin_text)
    text = out.stdout + out.stderr

    if out.returncode != 0:
        m = SERVERCERT_RE.search(text)
        if m and not profile.get("servercert"):
            return {"ok": False, "needs_fingerprint": True,
                    "fingerprint": m.group(1),
                    "msg": "The gateway's certificate is not trusted yet."}
        msg = _first_error(text)
        if any(k in text for k in _UNREACHABLE):
            msg = STALE_VPN_HINT
        return {"ok": False, "msg": msg}

    values = {}
    for line in out.stdout.splitlines():
        if "=" in line:
            key, _, val = line.partition("=")
            if key.strip() in COOKIE_KEYS:
                values[key.strip()] = val.strip().strip("'")
    if not values.get("COOKIE"):
        return {"ok": False, "msg": _first_error(text)}
    return {"ok": True, **values}


def _start_tunnel(profile, auth):
    """Run the tunnel half, detached, with ocbridge as the --script-tun target."""
    runtime = _state["runtime_dir"]
    # CONNECT_URL rather than HOST: the manual warns the second stage must
    # reach the same server the authentication ended at, after any redirects.
    target = auth.get("CONNECT_URL") or _url(profile["host"])
    args = ["--cookie-on-stdin", "--script-tun", "--script", _tool("ocbridge")]
    fingerprint = auth.get("FINGERPRINT") or profile.get("servercert")
    if fingerprint:
        args.append("--servercert=" + fingerprint)
    if auth.get("RESOLVE"):
        args.append("--resolve=" + auth["RESOLVE"])
    args.append(target)

    env = dict(os.environ,
               OCBRIDGE_PKG=PKG,
               OCBRIDGE_DATA_DIR=_state["data_dir"])

    # start_new_session is not optional. Lomiri SIGSTOPs unfocused apps, and a
    # process group of its own is what keeps the tunnel alive once the user
    # switches away -- which is most of the time a VPN is wanted at all.
    proc = subprocess.Popen(
        [_tool("openconnect"), "--protocol=" + PROTOCOL, "--useragent=" + USER_AGENT] + args,
        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
        stderr=open(os.path.join(runtime, "openconnect.log"), "wb"),
        env=env, start_new_session=True)
    try:
        # The cookie goes over stdin, never in argv: /proc/<pid>/cmdline is
        # world readable.
        proc.stdin.write((auth["COOKIE"] + "\n").encode())
        proc.stdin.flush()
        proc.stdin.close()
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        proc.kill()
        return {"ok": False, "msg": "Could not hand the cookie to openconnect: %s" % exc}

    _write_atomic(os.path.join(runtime, "openconnect.pid"), str(proc.pid) + "\n", 0o644)

    # Wait for ocbridge to actually come up before calling this a success.
    #
    # openconnect does NOT exit when its --script-tun child dies at startup: it
    # sits there with the Cisco session established and a <defunct> child,
    # nothing listening, and the failure only shows up much later as the
    # system VPN client timing out against a server that never existed. Worse,
    # the orphan holds a VPN session open indefinitely. So verify, and if the
    # bridge never appears, tear the whole thing down and say why.
    deadline = time.time() + 20
    while time.time() < deadline:
        if _alive(_read_pid("ocbridge.pid")) and os.path.exists(
                os.path.join(runtime, "status.json")):
            return {"ok": True}
        if proc.poll() is not None:
            break
        time.sleep(0.25)

    _terminate(proc.pid)
    return {"ok": False, "msg": _tunnel_failure_reason(runtime)}


def _tunnel_failure_reason(runtime):
    """Explain a bridge that never started, using openconnect's own output."""
    try:
        with open(os.path.join(runtime, "openconnect.log")) as fh:
            tail = [l.strip() for l in fh.read().splitlines() if l.strip()][-4:]
    except Exception:  # noqa: BLE001 - no log is itself not informative
        tail = []
    if any("Permission denied" in l for l in tail):
        return ("The tunnel helper could not be started (permission denied). "
                "Check `dmesg | grep DENIED` for an AppArmor denial.")
    return ("The tunnel helper did not start. " + " ".join(tail)).strip()


def _terminate(pid, grace=5.0):
    if not pid or not _alive(pid):
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except OSError:
        return
    deadline = time.time() + grace
    while time.time() < deadline and _alive(pid):
        time.sleep(0.1)
    if _alive(pid):
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass


def disconnect():
    """Stop the tunnel.

    Signals openconnect rather than ocbridge: openconnect tears the script down
    with kill(-pid, SIGHUP) on the way out, and ocbridge additionally arms
    PR_SET_PDEATHSIG, so stopping the parent is enough and leaves nothing
    behind.
    """
    try:
        pid = _read_pid("openconnect.pid")
        if pid and _alive(pid):
            os.kill(pid, signal.SIGTERM)
            return {"ok": True}
        # Fall back to ocbridge if openconnect is already gone but the bridge
        # somehow is not.
        pid = _read_pid("ocbridge.pid")
        if pid and _alive(pid):
            os.kill(pid, signal.SIGTERM)
            return {"ok": True}
        return {"ok": True, "msg": "Nothing was running."}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "msg": str(exc)}


def _read_pid(name):
    try:
        with open(os.path.join(_state["runtime_dir"], name)) as fh:
            return int(fh.read().strip())
    except Exception:  # noqa: BLE001 - absent or unreadable means "not running"
        return 0


def _alive(pid):
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def status():
    """Read whatever ocbridge last published.

    A status file with no live bridge behind it is reported as down: the tunnel
    outlives the UI, so a stale file from a previous run is expected rather
    than exceptional.
    """
    try:
        path = os.path.join(_state["runtime_dir"] or "", "status.json")
        if not os.path.exists(path):
            return {"ok": True, "state": "down"}
        with open(path) as fh:
            data = json.load(fh)
        if not _alive(_read_pid("ocbridge.pid")):
            data["state"] = "down"
        return {"ok": True, **data}
    except Exception as exc:  # noqa: BLE001 - surfaced to the UI
        return {"ok": False, "state": "error", "msg": str(exc)}
