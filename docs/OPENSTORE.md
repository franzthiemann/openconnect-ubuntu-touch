# OpenStore submission

Everything needed for <https://open-store.io/submit>. The app **name** must
match the manifest `name` exactly, or the upload is rejected.

| Field | Value |
|---|---|
| Name | `ocvpn.franzthiemann` |
| Title | OpenConnect VPN |
| Category | Utilities |
| Licence | GPL-3.0-or-later |
| Source | https://github.com/franzthiemann/openconnect-ubuntu-touch |
| Maintainer | Franz Thiemann &lt;franz@thiemann.io&gt; |
| Framework | ubuntu-touch-24.04-1.x |
| Architectures | arm64 (amd64 builds too, for development) |

## Tagline

Connect to Cisco AnyConnect (OpenConnect) VPNs.

## Description

Ubuntu Touch can only speak OpenVPN and PPTP, so Cisco AnyConnect VPNs — the
kind most universities and companies run — have been out of reach. This app
adds them.

It connects to the gateway, and hands the tunnel to Ubuntu Touch's own VPN
support to switch on and off. It runs as a normal confined app: it never asks
for your device password, never needs root, and changes nothing on your system.

Features:

- Several saved VPNs, each with its own gateway, user and login group
- Login groups fetched from the gateway before you sign in
- Gateway certificates confirmed once and then pinned
- The routes, DNS and MTU your VPN provides are applied by the system

**Three one-time setup steps are needed**, and the app walks you through them on
first launch. Briefly: exempt the app from suspension in UT Tweak Tool, create
one OpenVPN connection in Settings using the values the app shows, and tick
"Only use connection for VPN resources".

Known limitation: split tunnels only. Traffic for the networks your VPN serves
goes through it; everything else goes out normally. Routing *all* traffic
through the VPN is not supported — see the README for why.

## Review notes for the OpenStore maintainer

This should pass automatic review; it is worth saying why a VPN app does not
need special permissions.

- AppArmor: `networking` only. No reserved policy groups, no `unconfined`
  template. `clickable build` ends with `click-review: pass`.
- The app performs **no privileged operation**. `openconnect` runs with
  `--script-tun`, which keeps the tunnel in userspace with no tun device and no
  `CAP_NET_ADMIN`. The tunnel is re-exposed as an OpenVPN server bound to
  `127.0.0.1`, and NetworkManager's own OpenVPN client — started by the user
  from Settings — creates the real interface and installs the routes.
- Three binaries are bundled and built from source by `tools/prebuild.sh`, with
  the upstream tarballs pinned by SHA-256:
  - `openconnect` 9.12 (LGPL-2.1), patched — `packaging/openconnect/`
  - `openvpn` 2.6.19 (GPL-2.0), patched — `packaging/openvpn/`
  - `ocbridge`, this project's own Go binary
  Both patches and the build recipe are in the repository as the corresponding
  source. No shared libraries are bundled; everything linked is already in the
  rootfs.

## Screenshots

In `docs/screenshots/`, sanitised and ready to attach, in this order:

| File | Shows |
|---|---|
| `1-profile-list.png` | The profile list, not connected |
| `2-tunnel-ready.png` | Tunnel up, with the values to enter in Settings shown inline |
| `3-one-time-setup.png` | The setup page reached from the first-run screen |
| `4-system-vpn-editor.png` | The matching connection in the system VPN editor |

They were redacted with `tools/sanitize-screenshots.py`: the generated OpenVPN
username and password, and the gateway and account of the VPN they were taken
against, are overwritten with realistic fakes drawn in the device's own Ubuntu
font. Solid overwrite, not blur — blurred text can often be recovered. Source
metadata is stripped by the re-save.

If you take more, note that `clickable screenshots` pulls the **entire**
`~/Pictures/Screenshots/` directory from the device, personal ones included.
