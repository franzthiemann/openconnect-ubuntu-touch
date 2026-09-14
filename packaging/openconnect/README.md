# Patched OpenConnect

The click ships a modified `openconnect`. OpenConnect is **LGPLv2.1**, so this
directory is the corresponding source offer:

- `0001-script-tun-direct-exec.patch` — the modification, against openconnect 9.12.
- `../../tools/prebuild.sh` — the exact build recipe, including the pinned
  upstream tarball URL and its SHA-256.

Unmodified upstream source:
<https://www.infradead.org/openconnect/download/openconnect-9.12.tar.gz>

## What the patch does

`--script-tun` spawns its program with

    execl("/bin/sh", "/bin/sh", "-c", script, NULL);

A confined Ubuntu Touch click may execute anything inside its own package tree
but **nothing in `/usr/bin`**, including the shell. So the fork always died with

    apparmor="DENIED" operation="exec" name="/usr/bin/dash" comm="openconnect"
    execl: Permission denied

with the Cisco tunnel already established and the script child immediately
`<defunct>` — nothing listening, and NetworkManager timing out against a server
that never started.

The patch execs the program directly when the `--script` string is a plain path
with no shell syntax in it. Anything containing arguments, quoting or
redirection (`ocproxy -D 1080 ...`, `sudo -E vpnc-script`) still goes through
the shell exactly as before, so existing usage is unaffected.

This is worth offering upstream: any sandboxed caller — flatpak, snap, or a
click — hits it.
