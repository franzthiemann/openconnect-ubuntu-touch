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
| Architectures | arm64 |

---

## Tagline

> Cisco AnyConnect VPNs, without root

---

## Description

Ubuntu Touch speaks only OpenVPN and PPTP, so the Cisco AnyConnect VPNs that most universities and companies run have been out of reach. This app adds them.

Enter your gateway, username and password. The app brings the tunnel up, and Ubuntu Touch's own VPN support carries it from there — so the routes, DNS and MTU your organisation provides are applied by the system exactly as they are for any other VPN. It runs as an ordinary confined app. It never asks for your device password, never needs root, and changes nothing on your system.
**What it does**
- Several saved VPNs, each with its own gateway, account and login group
- Login groups fetched from the gateway before you sign in, so you choose from
- a list instead of guessing
- Gateway certificates shown once for you to check, then remembered
- Passwords optional — saved per VPN, or typed each time
**Before you start**
Three one-time steps are needed. The app shows all three on first launch:
1. Exempt the app from suspension in UT Tweak Tool. (VPNs need to run in the background)
2. Create one OpenVPN connection in Settings, using the values the app displays. Tap any value to copy it.
3. Tick "Only use connection for VPN resources" in that connection's Advanced settings.

After that, connecting is: open the app, tap your VPN, then switch it on in Settings.

**Known limitation**
Split tunnels only. Traffic for the networks your VPN serves goes through it; everything else goes out normally. Routing *all* traffic through the VPN is not supported yet

Source and a full account of how it works:
https://github.com/franzthiemann/openconnect-ubuntu-touch

---

## Release message (1.0.0)

> First release.
>
> Connects Ubuntu Touch to Cisco AnyConnect (OpenConnect) VPNs — something the
> system cannot do on its own, since it speaks only OpenVPN and PPTP.
>
> Runs as an ordinary confined app: no root, no device password, and nothing
> changed on your system. The AnyConnect tunnel is handled in userspace and
> handed to Ubuntu Touch's own VPN support, which applies the routes and DNS.
>
> Three one-time setup steps are needed and the app walks you through them on
> first launch. Split tunnels only.
>
> Tested on a Fairphone 5 running Ubuntu Touch 24.04-1.x.

---

## Review notes for the OpenStore maintainer

This should pass automatic review; it is worth saying why a VPN app needs no
special permissions.

- AppArmor: `networking` only. No reserved policy groups, no `unconfined`
  template. `clickable build` ends with `click-review: pass`.
- The app performs **no privileged operation**. `openconnect` runs with
  `--script-tun`, which keeps the tunnel in userspace with no tun device and no
  `CAP_NET_ADMIN`. The tunnel is re-exposed as an OpenVPN server bound to
  `127.0.0.1`, and NetworkManager's own OpenVPN client — started by the user
  from Settings — creates the real interface and installs the routes.
- Three binaries are bundled, all built from source by `tools/prebuild.sh` with
  the upstream tarballs pinned by SHA-256:
  - `openconnect` 9.12 (LGPL-2.1), patched — `packaging/openconnect/`
  - `openvpn` 2.6.19 (GPL-2.0), patched — `packaging/openvpn/`
  - `ocbridge`, this project's own Go binary
  Both patches and the build recipe are in the repository as the corresponding
  source. No shared libraries are bundled; everything linked is already in the
  rootfs.

## Banner

`assets/banner.svg`, and `assets/banner.png` rendered at 1280x640 for anywhere
that will not take SVG — GitHub's social preview, forum posts, and the store
form if it offers a slot for one. Same layout as the author's other Ubuntu
Touch app so the two read as a pair.

GitHub's social preview cannot be set through the API; upload `assets/banner.png`
by hand under Settings -> General -> Social preview.

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
