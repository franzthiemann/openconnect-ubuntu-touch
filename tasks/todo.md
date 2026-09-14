# openconnect_ubt — task plan

STATUS: Phases 0-3 COMPLETE. Confined --script-tun needed an openconnect
patch (/bin/sh is not executable under confinement); fixed and verified on
device -- ocbridge survives and 127.0.0.1:1194 listens. Remaining: switch the
VPN on in Settings and confirm traffic flows.

Full plan: ~/.claude/plans/i-want-to-create-rippling-sparrow.md

Architecture: confined click. openconnect --script-tun -> ocbridge (Go) ->
patched openvpn (TLS server, no tun) on 127.0.0.1:1194 <- UT's NetworkManager
OpenVPN client (root) creates the real tun. App never needs root.

## Phase 0 — de-risk on the desktop (no phone, no click)

- [x] 0.1 Test rig: ocserv in Docker (tests/rig/), user testuser/testpass
- [x] 0.2 script-tun PROVEN: AF_UNIX/SOCK_DGRAM, exact framing, ICMP round-trip
- [x] 0.3 Real gateway probed: NO CSD/HostScan. --useragent MANDATORY on 9.12
      (404->legacy fallback without it). Auth GROUP required; discoverable
      with no credentials. See lessons.md
- [x] 0.4 Patched openvpn 2.6.19 chosen and working (packaging/openvpn/, 41-line patch)
- [x] 0.5 `net_gateway` DOES resolve under `--route-noexec` (source-verified).
      Also found: `push "redirect-gateway"` is a NO-OP under NM. See lessons.md
- [x] 0.6 ifconfig-push hands the client our address -> NO NAT NEEDED
- [x] 0.7 Route env verified against an NM-shaped client (6/6); loop fix proven
- [x] 0.8 FULL CHAIN PROVEN (tests/rig/e2e_test.py, 10/10): ICMP from the
      'phone' end reaches the gateway and the reply comes back. No root.

## Phase 1 — ocbridge (Go) — DONE
- [x] 1.1 VPNFD relay; SIGHUP/SIGTERM teardown + PR_SET_PDEATHSIG liveness
- [x] 1.2 Config + PKI generation (stable CA, reused forever)
- [x] 1.3 status.json + pidfile for UI re-attach (file, not socket: survives
      the app being killed, which a socket would not)
- [x] 1.4 Unit tests (17) + e2e integration test

## Phase 2 — click packaging — DONE
- [x] 2.1 clickable.yaml, CMakeLists, manifest/apparmor/desktop templates
- [x] 2.2 openconnect built from source WITHOUT libproxy -> no bundled .so at
      all (was 13 libs + a 30-lib curl subtree). Click is 1.9 MB.
- [x] 2.3 All three natives cross-built for arm64 by tools/prebuild.sh
      (single build path; checksums pinned and actually verified)
- [x] 2.4 Launcher (built-ins only; deliberately does NOT export
      LD_LIBRARY_PATH into qmlscene)
- [x] 2.5 click-review PASSES with policy_groups ["networking"] only
- [x] 2.6 tools/verify-click.sh: dangling symlinks, ELF arch, build droppings,
      NEEDED closure (+ adb diff when a device is attached)
- [x] 2.7 e2e re-run against the SHIPPING binaries in an Ubuntu 24.04
      userspace (tests/rig/run-e2e.sh): 10/10

## Phase 3 — app (QML + PyOtherSide) — DONE
- [x] 3.7 First-run setup screen (suspension exemption, VPN profile, routing)
- [x] 3.1 backend.py: profiles, auth, connect/disconnect, status
- [x] 3.2 QML: ProfileListPage, ProfileEditPage, SetupPage (copy-to-clipboard)
- [x] 3.3 Cert TOFU: scrape the pin, confirm with the user, store per profile
- [x] 3.4 tests/test_backend.py against the rig (19 checks)
- [x] 3.5 Auth-group picker, groups discovered with no credentials
- [x] 3.6 VERIFIED ON DEVICE against vpn.example.edu: groups discovered,
      tunnel up (<tunnel-address>, mtu 1390, 7 split routes), clean disconnect

## Phase 4 — device verification (partly done early, device was attached)
- [x] 4.0 Click installs and launches on a Fairphone 5 / 24.04-1.x
- [x] 4.1 All three arm64 binaries run; every NEEDED library present on rootfs
- [x] 4.2 backend.init() works confined; only benign AppArmor denials remain
- [x] 4.3 Cisco side proven on device against the real gateway
- [x] 4.4 Suspension: start_new_session does NOT protect; lifecycle exemption
      required. Applied on the test device.
- [ ] 4.4b Re-attach after app restart, teardown
- [x] 4.5a VPN editor can browse to and select the app's ca.crt (UX risk closed)
- [x] 4.5b Confined --script-tun fixed (openconnect patched); server listens
- [x] 4.5c Transport mismatch found and fixed (UT editor writes proto-tcp=yes;
      ocbridge now defaults to TCP). Server confirmed LISTENing on TCP.
- [x] 4.5d Two blockers found and fixed: (a) auth helper needed /usr/bin/sed,
      now ocbridge --auth in Go; (b) Lomiri suspends the whole process tree,
      needs a lifecycle exemption (documented as setup step 1)
- [x] 4.5e OpenVPN connection works end to end (user confirmed)
- [x] 4.6 Routing bug found: pushed bypass route is bound to tun0 by NM and
      loops the transport into its own tunnel. Push removed.
- [x] 4.7 Split routing works; internet restored
- [x] 4.8 Gateway sits inside its own pushed split route (198.18.0.0/16 ->
      198.18.110.147). ExcludeHost punches it out. Needs on-device confirm.
- [ ] 4.9 Confirm uni resources reachable with the hole-punched routes
