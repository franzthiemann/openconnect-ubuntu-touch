# Changelog

## 1.0.0 — 2026-09-14

First release.

Connects Ubuntu Touch to Cisco AnyConnect (OpenConnect) VPNs, which the system
cannot do on its own: Ubuntu Touch knows only OpenVPN and PPTP, and
`openconnect` is not in the rootfs.

- Terminates the AnyConnect tunnel entirely in userspace and re-exposes it as a
  local OpenVPN server, so the system's own VPN client does the privileged work.
  The app ships as an ordinary **confined** click — no root, no `sudo`, and no
  modification to the device.
- Several saved VPN profiles, with the login group fetched from the gateway
  before signing in.
- Trust-on-first-use for gateway certificates: the fingerprint is shown for
  confirmation and then pinned per profile.
- First-run screen covering the three one-time setup steps.
- Split routes, DNS and MTU from the gateway are carried through to the system.
