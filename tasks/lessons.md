# Lessons

## OpenVPN route handling under NetworkManager's `--route-noexec`

Verified by reading openvpn 2.6.19 source (`src/openvpn/route.c`, `init.c`).
NM-openvpn always passes `--route-noexec` and recovers routes from the `--up`
environment, so what that flag does and does not suppress is decisive for us.

- **`push "route X Y net_gateway"` works.** `route_noexec` guards only
  `add_routes()` inside `do_route()` (`init.c`). The environment export is a
  different path with no such check: `do_init_route_list()` calls
  `init_route_list()` and then `setenv_routes()` unconditionally.
  `init_route_list()` calls `get_default_gateway(&rl->rgi, ctx)` at the top, and
  `init_route()` resolves each route's gateway string through
  `get_special_addr()`, whose `net_gateway` branch returns `rl->rgi.gateway.addr`.
  `setenv_route()` then exports that as `route_gateway_%d`.
  → **This is what breaks the routing loop, and it is safe.**
- **`push "redirect-gateway def1"` is a NO-OP under NetworkManager.**
  `redirect_default_route_to_vpn()` is called from exactly one place —
  `add_routes()` (route.c:1186) — which `--route-noexec` skips. The /1 routes are
  therefore never created and never exported. NM decides the default route itself
  from its own `never-default` property, which is the only routing control the UT
  VPN editor exposes ("Only use connection for VPN resources"). So do not push
  redirect-gateway; push the split routes plus the gateway bypass, and let the
  user's checkbox choose full vs split tunnel.
- Minor: `test_local_addr()` on Linux asks `local_route()` whether the address is
  on the default gateway's subnet, so `127.0.0.1` classifies as **TLA_NONLOCAL**.
  Harmless — it only means openvpn would have added a useless `127.0.0.1/32` via
  the real gateway, and under `--route-noexec` it does not even do that.

## `--script-tun` verified empirically (against ocserv in Docker)

The whole unprivileged data path is proven: no tun device, no root, no
capabilities, plain user, and an ICMP echo request injected into `VPNFD` came
back as a reply. Details worth keeping:

- `VPNFD` is an **`AF_UNIX` / `SOCK_DGRAM`** socket, as the openconnect source
  said. **Framing is exact**: the datagram length equals the IP header's
  `total_len`, with no length prefix and no 4-byte AF prefix. So ocbridge needs
  no re-framing at all on this side — one `recv()` is one IP packet.
  (Contrast with `wg-ovpn`, which relays over a *pty*, i.e. a byte stream, and
  has to re-frame on the IPv4 total-length field. That is why it is unstable.
  Do not copy that approach.)
- The full vpnc-script environment is present, confirmed live:
  `INTERNAL_IP4_ADDRESS`, `INTERNAL_IP4_NETMASK`, `INTERNAL_IP4_NETMASKLEN`,
  `INTERNAL_IP4_NETADDR`, `INTERNAL_IP4_DNS`, `INTERNAL_IP4_MTU`, `VPNGATEWAY`,
  `VPNPID`, and `CISCO_SPLIT_INC_%d_{ADDR,MASK,MASKLEN}` per split route.
  Note the split-route family carries **both** `MASK` (dotted) and `MASKLEN`,
  so the openvpn `push "route A B"` line needs no netmask conversion.
- Packets sourced from `INTERNAL_IP4_ADDRESS` are accepted by the gateway and
  answered — which is what makes the "hand the OpenVPN client that same address
  so no NAT is needed" plan worth pursuing first.
- When the script exits while openconnect is still up, openconnect dies with
  `Failed to write incoming packet: Connection refused` then
  `Unrecoverable I/O error; exiting`. ocbridge must therefore outlive the
  tunnel and drain the socket for as long as openconnect runs.

## Host environment

- **Docker BuildKit on this machine is corrupt**: any `docker build` fails with
  `failed to add snapshot ... to lease: structure needs cleaning` (EUCLEAN).
  `docker pull` and `docker run` are fine, and `DOCKER_BUILDKIT=0 docker build`
  works. Use the legacy builder, or repair with `docker builder prune -af`.

## `--dev-node fd:N`: openvpn runs unprivileged with no tun

`packaging/openvpn/0001-dev-node-fd.patch` (41 added lines against 2.6.19) adds
one `else if` branch to the Linux `open_tun()` that adopts an inherited
descriptor instead of `open("/dev/net/tun")` + `ioctl(TUNSETIFF)`.

Why it is this small and this safe:
- On Linux openvpn opens tun with **`IFF_NO_PI`** (tun.c:2223), so `read_tun()` /
  `write_tun()` are a bare `read()`/`write()` of *unprefixed* IP packets --
  byte-identical to what openconnect's `--script-tun` socketpair carries. No
  translation layer is needed in either direction.
- A `SOCK_DGRAM` socketpair preserves packet boundaries, so one `read()` is one
  packet, exactly as for a real tun device.
- The only other ioctls on `tt->fd` on Linux are `TUNSETPERSIST`/`TUNSETOWNER`/
  `TUNSETGROUP` in `tuncfg()`, which is the `--mktun` path we never take.
- Build with **`--disable-dco`**: with kernel offload openvpn never touches
  `tt->fd` at all, and `tun_dco_enabled(tt)` is tested *before* our branch.
- `libcap-ng-dev` is a mandatory build dep on Linux; `libcap-ng` must be bundled
  or present on the device.

Verified by `tests/rig/ovpn_fd_test.py` (4/4): a patched server *and* a patched
client, both on inherited socketpairs, complete a TLS handshake and a packet
injected at one fd arrives byte-exact at the other. No tun, no root, no
capabilities anywhere.

## Client addressing: `ifconfig-push` removes the need for NAT

`tests/rig/ovpn_fd_test.py` confirms a `client-config-dir` entry containing
`ifconfig-push <addr> <mask>` makes the client take exactly that address:
`PUSH_REPLY,...,ifconfig 10.99.0.121 255.255.255.0`. So ocbridge can hand the
OpenVPN client the Cisco-assigned `INTERNAL_IP4_ADDRESS`, and the 1:1
source/destination rewriting with checksum fixups is **not needed**. Keep the
NAT fallback out of v1.

Gotcha: `ccd-exclusive` requires a ccd file named after the common name and
ignores `DEFAULT`. Either drop `ccd-exclusive` (then `DEFAULT` applies to every
client, which is what we want with a single client) or name the file after the
username. With `verify-client-cert none` + `username-as-common-name`, the CN is
the username.

## Empirical confirmation of the route environment (NM-shaped client)

`tests/rig/ovpn_route_env_test.py` (6/6) runs a *stock, unpatched* client on
`--dev null` -- no tun, no root -- with `--route-noexec --ifconfig-noexec`, i.e.
configured exactly as NetworkManager-openvpn configures one, and dumps the
`--up` environment. Observed:

    route_net_gateway=10.42.0.1        <- the host's real default gateway
    route_network_1=203.0.113.77  route_netmask_1=255.255.255.255
    route_gateway_1=10.42.0.1          <- "net_gateway" RESOLVED
    route_network_2=10.200.0.0    route_netmask_2=255.255.0.0
    route_gateway_2=10.99.0.1
    route_vpn_gateway=10.99.0.1
    foreign_option_1=dhcp-option DNS 10.99.0.53
    foreign_option_2=dhcp-option DOMAIN rig.test

- `push "route <gw> 255.255.255.255 net_gateway"` **works** under
  `--route-noexec`. This is the routing-loop fix, now proven end to end.
- `push "redirect-gateway def1"` produced **no** /1 routes, confirming it is a
  no-op here. Full vs split tunnel is the user's "Only use connection for VPN
  resources" checkbox, not something we push.
- `--dev null` is a useful trick: it exercises the whole pull/route/up path with
  no tun and no privileges, so NM-shaped behaviour can be regression-tested on
  any machine.

## The real gateway (vpn.uni-leipzig.de) — Phase 0.3

Probed live. Everything needed for the app's auth design:

- **No CSD/HostScan.** Clean `XML POST enabled`. The one risk that could have
  killed the project is absent; no `--csd-wrapper`, no bundled Python.
- **The User-Agent trap is REAL here.** noble's openconnect **9.12** with its
  default UA (`Open AnyConnect VPN Agent v9.12`) gets
  `HTTP/1.1 404 Not Found` / `Unexpected 404 result from server` and silently
  falls back to the legacy `/+webvpn+/index.html` path. It still authenticates,
  but over the obsolete route upstream warns is frequently untested.
  With `--useragent 'AnyConnect-compatible OpenConnect VPN Agent v9.12'`:
  **zero 404s**, `XML POST enabled`, and auth goes to `https://.../`.
  → The click MUST always pass `--useragent`. Non-negotiable, and invisible
  without `-v`, because the fallback succeeds.
  (Host openconnect 9.21 does not show this — its default UA already starts
  with `AnyConnect`. Never conclude anything about the device from 9.21.)
- **An auth group MUST be selected**, and it is prompted *before* username and
  password:
  `GROUP: [1-Standard-Uni|2-Spezial-Alles|3-Test-MFA]:`
    - `1-Standard-Uni`  = only university services over the VPN (split tunnel)
    - `2-Spezial-Alles` = everything over the VPN (e.g. library resources)
    - `3-Test-MFA`      = the MFA group, for when 2FA lands
  Without `--authgroup`, `--passwd-on-stdin` has already eaten the single stdin
  line at option-parse time, so the GROUP prompt hits EOF and openconnect exits
  with `User input required in non-interactive mode`. Always pass
  `--authgroup=<group>`.
- **Groups can be discovered with NO credentials**: run
  `openconnect --protocol=anyconnect --authenticate --non-inter <host>` and
  scrape the `GROUP: [a|b|c]:` line from stderr. The app should do this to
  populate a group picker before asking for a password — same scrape-stderr
  trick as the certificate TOFU pin.
- Server fingerprint (for pinning):
  `pin-sha256:L54CpUv9VK4L3U6aGsp4yDX47MaHpizZOjuLDhWgzis=`,
  host 139.18.110.147, `CONNECT_URL='https://vpn.uni-leipzig.de/'`.
- Design consequence: the **group picker is a required first-class UI element**,
  not an advanced option, and the profile model needs an `authgroup` field.
  The group also effectively chooses split vs full tunnel on the Cisco side,
  which composes with NM's "Only use connection for VPN resources" checkbox.

## Closing a SOCK_DGRAM peer does NOT wake the reader (the ocbridge liveness trap)

The single most dangerous thing found while building the relay. On Linux,
closing one end of an `AF_UNIX`/**`SOCK_DGRAM`** socketpair leaves a blocked
reader on the other end blocked **forever**: no `EOF`, no `POLLHUP`, nothing.
Unlike `SOCK_STREAM`, where the read returns 0.

Two layers conspire to hide it:
- Go's `net` package sets `ZeroReadIsEOF` only for non-`SOCK_DGRAM` sockets
  (`net/fd_unix.go`), so even a genuine zero-length read comes back as
  `(0, nil)` rather than `io.EOF`. A `continue` on `n == 0` is an infinite busy
  loop.
- And the read never even returns, so that path is not reached anyway.

Consequence: **the relay cannot detect that openconnect died.** A killed
openconnect would leave ocbridge relaying into the void with the UI still
showing "up". Fixes, both now in the code:
- `prctl(PR_SET_PDEATHSIG, SIGTERM)` (`parent.go`), re-checking `getppid()`
  afterwards to close the race where the parent died before the prctl landed.
- Never rely on a peer close; shutdown always closes *our own* descriptors.

`TestPeerCloseIsNotDetectableOnADatagramSocket` pins the kernel behaviour down
so that a future reader does not "simplify" the liveness handling away. If that
test ever fails, the platform changed.

## openvpn server config gotchas (each cost a failed run)

- **`ifconfig-pool` must span at least two addresses.** openvpn validates it at
  startup: `IPv4 pool size is too small (1), must be at least 2` then `Exiting
  due to fatal error`. Pinning the client's address with a single-slot pool does
  not work -- pin it with a **ccd** `ifconfig-push` and give the pool the whole
  usable remainder of the subnet. This is why ocbridge needs a /29 or larger
  from the gateway; a /30 cannot host both the local endpoint and a 2-address
  pool.
- `dh none` is required with modern openvpn unless a DH file is supplied.
- `verify-client-cert none` + `username-as-common-name` +
  `auth-user-pass-verify <helper> via-file` is what reduces the user's manual
  setup to **one** file picker (the CA) plus a username and password.

## Rig gotcha

`docker run -d` returns as soon as the container is *created*, not when the
service inside is listening. Probing immediately gets connection-refused and
looks like a broken rig. `rig.sh up` now polls `/dev/tcp/127.0.0.1/4443` (a bash
builtin, hence the bash shebang) until ocserv answers.

## Packaging (Phase 2)

### Build openconnect from source, do not bundle the distro one

Ubuntu's `openconnect` links **libproxy**, whose PAC backend
(`libpxbackend-1.0.so`) pulls in `libcurl-gnutls` and `libduktape` and,
transitively, about thirty libraries: brotli, ldap, sasl, ssh, rtmp, nghttp2,
psl, zstd. `apt-cache rdepends --installed libproxy1v5` showed openconnect is
the *only* thing that wanted it, so it is not something the rootfs is likely to
provide either. Nothing in this app uses a proxy auto-config script.

Building openconnect with

    --without-gnutls --without-libproxy --without-stoken --without-libpcsclite
    --without-libpskc --without-gssapi --disable-nls
    --disable-shared --enable-static

yields **one 461 KB binary** whose entire NEEDED set is base-system:
`libssl`, `libcrypto`, `libxml2`, `liblz4`, `libz`, `libm`, `libc`. The click
therefore bundles **no shared libraries at all** and dropped from 3.0 MB to
1.9 MB. OpenSSL rather than GnuTLS so openconnect and openvpn share one TLS
stack.

### install_lib globs: soname version != file version

`libproxy.so.1` is a symlink to `libproxy.so.0.5.4`. A `libproxy.so.1*` glob
matches only the symlink, so the click shipped a **dangling** link that would
have failed at load time on the device with a missing-library error. Any glob
written from the soname is suspect; check what the link actually points at.
`tools/verify-click.sh` now fails the build on any dangling symlink, which is
how this was caught.

### Other packaging facts, verified

- Clickable's `install_bin` lands in `${INSTALL_DIR}/lib/${ARCH_TRIPLET}/bin`
  and `install_lib` in `${INSTALL_DIR}/lib/${ARCH_TRIPLET}` — both under lib/,
  which is not obvious from the names.
- `install(DIRECTORY src ...)` happily ships `__pycache__`. The host's bytecode
  was Python **3.14**; the device runs 3.12. Exclude it explicitly.
- The AppArmor template grants
  `owner @{HOME}/.local/share/@{APP_PKGNAME}/** mrwklix` — note the **`ix`**:
  the app's data directory *is* executable. `~/.cache` and `~/.config` get
  `mrwkl` and are not. (An earlier comment in `bin/ocbridge-auth` claimed the
  opposite; corrected.)
- The confined runtime directory is `/run/user/*/confined/@{APP_PKGNAME}/**`,
  so `XDG_RUNTIME_DIR` already ends in the package name — appending it again
  nests pointlessly. Both `ocbridge` and `backend.py` guard against that.
- `qmlscene` is explicitly execable from a confined profile
  (`/usr/lib/@{multiarch}/qt5/bin/qmlscene ixr`), as are all files under the
  package tree (`@{CLICK_DIR}/.../** mrklix`).
- **click-review passes** with `policy_groups: ["networking"]` alone, which was
  the entire point of the confined architecture.

### The host cannot run the click's amd64 binaries

Manjaro's libxml2 has a different soname, so an Ubuntu-linked `openconnect`
fails with `libxml2.so.2: cannot open shared object file`. The end-to-end test
therefore runs **inside an Ubuntu 24.04 container** (`tests/rig/run-e2e.sh`),
which is both the fix and a better test: it exercises the shipping binaries
against the same library versions the device has.

No aarch64 binfmt is registered on this machine, so arm64 binaries cannot be
executed here at all. They are verified statically (ELF machine, resolved
symlinks, dependency closure) and functionally via the identical amd64 build.

## First on-device run (Fairphone 5, 24.04-1.x/arm64, build 1440)

All three arm64 binaries run on the device:
`openconnect v9.12` (OpenSSL 3.0.13, DTLS, anyconnect), `openvpn 2.6.19
aarch64` with `--dev-node` present, and `ocbridge` refusing to start outside
openconnect with its intended message. `tools/verify-click.sh` with the device
attached confirms **every unbundled NEEDED library is present on the rootfs**,
so the from-source build's dependency assumption holds in reality.

### Installing on UT 24.04

- **`pkcon` does not exist** on 24.04. `click install` does, but refuses:
  *"Cannot acquire permission to write to /opt/click.ubuntu.com"* — and `sudo`
  on this device **does** require a password.
- The working path is the one clickable already uses, a root-side D-Bus
  service, no password needed:

      gdbus call --system --dest com.lomiri.click --object-path /com/lomiri/click \
            --method com.lomiri.click.Install /home/phablet/<file>.click

- **`clickable install` reports `ADB_COMMAND_FAILED` even when it succeeds.**
  That D-Bus call returns an empty tuple `()` on success, which clickable reads
  as failure. Ignore the error and verify with
  `ls /opt/click.ubuntu.com/<pkg>/current/`. (Its debug line "Using UT 20.04
  install command" is also misleading — that branch is correct for 24.04.)
- `lomiri-app-launch` from `adb shell` cannot start a GUI app: it dies with
  *"QMirClientClientIntegration: connection to Mir server failed"*. Use
  `clickable launch`, which also prints an error but does start the app.
  Identify your own process by its cwd, not by name:
  `for p in $(pgrep qmlscene); do echo "$p $(readlink /proc/$p/cwd)"; done` —
  another app's qmlscene will otherwise look like yours.

### Confined runtime paths, as actually observed

A confined app sees **`XDG_RUNTIME_DIR=/run/user/32011`**, *not* the
`confined/<package>` path the template's TMPDIR comment suggests. The generated
profile (`/var/lib/apparmor/profiles/click_<pkg>_<app>_<ver>`, the only source
of truth) grants both:

    owner /{,var/}run/user/*/@{APP_PKGNAME}/   rw
    owner /{,var/}run/user/*/@{APP_PKGNAME}/** mrwkl
    owner /{,var/}run/user/*/confined/@{APP_PKGNAME}/ rw    # for TMPDIR, no /** rule

so `<XDG_RUNTIME_DIR>/<package>` is the correct place for the pidfile and
status file. `backend.init()` creating `~/.local/share/<package>/` 0700 was
confirmed working under confinement.

### Denials seen, and which matter

Most are ordinary Qt/Android-HAL probing noise (sysfs cpu/gpu nodes,
`/usr/local/lib/python3.12/dist-packages/`, `/proc/*/loginuid`). One was ours:

    apparmor="DENIED" operation="mkdir" name=".../src/__pycache__/"

PyOtherSide caching bytecode into the read-only click tree. Harmless, but a
recurring denial trains you to ignore the log that is your main diagnostic, so
the launcher now exports `PYTHONDONTWRITEBYTECODE=1`.

## A checker that passes vacuously is worse than no checker

`tools/verify-click.sh` was run with a relative path while `ar x` executed from
a scratch directory, so extraction silently failed — and four checks then
reported **PASS against an empty directory**. Fixed by resolving the path with
`readlink -f`, making extraction failure fatal, and refusing to report anything
if the extracted tree is empty. Worth remembering for any future check script.

## The architecture works on the device, against the real gateway

Run on the Fairphone 5 through `backend.py` and the installed click's own
binaries, against vpn.uni-leipzig.de:

    discover_groups -> ['1-Standard-Uni', '2-Spezial-Alles', '3-Test-MFA']
    address=<tunnel-address>  mtu=1390  dns=['172.18.100.2', '172.18.100.3']
    routes = 192.168.50.0/23, 10.5.1.0/24, 172.16.0.0/16, 172.26.0.0/15,
             172.18.0.0/16, 141.39.224.0/20, 139.18.0.0/16
    local OpenVPN server listening on 127.0.0.1:1194

Notes worth keeping:

- The gateway's MTU is **1390**, and ocbridge pushes it through as `tun-mtu`,
  so the OpenVPN client sizes its tunnel to what the Cisco side will carry. The
  OpenVPN leg runs over loopback, so its encapsulation costs CPU but nothing on
  the wire; the Cisco MTU is the only one that matters.
- `1-Standard-Uni` is a **split** tunnel: the gateway sends seven
  `CISCO_SPLIT_INC_*` routes and ocbridge republishes each as a `push "route"`.
  For a full tunnel the user picks `2-Spezial-Alles` *and* leaves "Only use
  connection for VPN resources" off in the VPN editor.
- Bringing the tunnel up **changes no routing at all** until the user switches
  the connection on in Settings. That makes on-device testing safe: there is no
  window in which the phone's networking is half-configured.
- A confined app really can exec its own bundled binaries: the app generated
  its CA and credentials by running `ocbridge -init` from inside the click, and
  wrote them to `~/.local/share/<package>/`. Verified from the app's own
  process, not from `adb shell`.
- `PYTHONDONTWRITEBYTECODE=1` in the launcher removed the recurring
  `__pycache__` denial; the only denials left are ordinary Qt noise
  (sysfs probing, `qtshadercache`).

## openconnect's --script-tun cannot work confined without a patch

The first real on-device connection failed in a way that looked like a
NetworkManager problem ("the VPN connection times out") but was not. Evidence
chain, in the order it was found:

1. `openconnect` was running and connected to the gateway, but its child was
   `[openconnect] <defunct>` and no `ocbridge` process existed.
2. Nothing was listening on 127.0.0.1:1194, so NM's client had nothing to talk
   to -- hence a timeout, several layers away from the cause.
3. `openconnect.log` said only `execl: Permission denied`.
4. The kernel log gave the real reason:

       apparmor="DENIED" operation="exec" name="/usr/bin/dash" comm="openconnect"

`open_script_tun()` always spawns the script with
`execl("/bin/sh", "/bin/sh", "-c", script, NULL)`, and a confined click may
execute anything **inside its own package tree** but nothing in `/usr/bin` --
including the shell. `/bin/sh` is `/usr/bin/dash` on Ubuntu.

Fix: `packaging/openconnect/0001-script-tun-direct-exec.patch` execs the
program directly when `--script` is a plain path with no shell syntax, falling
back to the shell for anything with arguments or quoting (so `ocproxy -D 1080`
and `sudo -E vpnc-script` still behave as before). Verified on device: ocbridge
now survives and the OpenVPN server listens.

Two lessons that generalise:

- **A timeout at the far end of a chain says nothing about where the fault is.**
  The visible symptom was in NetworkManager; the cause was an exec denial three
  processes away. Follow the process tree (`<defunct>` children are a strong
  hint) before theorising.
- **AppArmor denials go to the KERNEL log.** Neither the app journal nor the
  process's own stderr named the missing binary; `dmesg | grep DENIED` did, in
  one line. The `execl: Permission denied` on its own would have been a long
  guessing game.

This is worth offering upstream: any sandboxed caller (flatpak, snap, click)
hits it.

## The local transport must match, and a mismatch is invisible server-side

Second on-device failure, same symptom as the first ("the VPN times out") and
again nothing to do with NetworkManager being at fault. The system journal had
the answer in one line:

    nm-openvpn: Attempting to establish TCP connection with [AF_INET]127.0.0.1:1194
    nm-openvpn: TCP: connect to [AF_INET]127.0.0.1:1194 failed: Connection refused

and the profile confirmed it:

    nmcli -f vpn.data connection show '127.0.0.1'
    ... connection-type = password, proto-tcp = yes, remote = 127.0.0.1 ...

**The Ubuntu Touch VPN editor writes `proto-tcp = yes`**, while ocbridge was
generating a `proto udp` server. The port was open the whole time -- for the
other protocol -- so `ss` looked healthy, and the server logged *nothing at
all*, because no packet ever reached it. Nothing on our side could observe the
problem; only the client's log named it.

ocbridge now takes `-proto` (env `OCBRIDGE_PROTO`) and **defaults to TCP**, to
match what the platform's own editor produces. Over loopback the choice costs
nothing either way. `status.json` reports the transport, and both the setup page
and the inline card state it as an instruction ("tick Use a TCP connection"),
because it is the one field where a wrong value produces a silent timeout
rather than an error.

The e2e test now reads the transport out of `status.json` and configures its
client to match, so the test tracks the product instead of hard-coding UDP.

### Corollary: check the client's log, not just your own

Both on-device failures were diagnosed from logs belonging to something *else*
-- the kernel audit log for the exec denial, the system journal for the
transport mismatch. The app's own logs were silent or misleading in both cases.
For a component that is deliberately passive (a server waiting to be connected
to), "no output" is not evidence of health.

### Runtime directory differs between adb and the app

Started from `adb shell` there is no `XDG_RUNTIME_DIR`, so ocbridge falls back
to `<data-dir>/run` and the app -- which uses `/run/user/32011/<package>` --
cannot see that tunnel at all. Harmless, but set `XDG_RUNTIME_DIR=/run/user/32011`
in on-device test scripts or you will chase a tunnel the app swears is down.

## The profile-entry exec is exempt; every later exec is not

This explains an apparent contradiction. The app's launcher is `#!/bin/sh` and
runs fine, yet openconnect's `execl("/bin/sh", ...)` was denied. Both are the
same binary (`/bin/sh` resolves to `/usr/bin/dash`).

The difference is *when* the exec happens. `lomiri-app-launch` starts the app
through `aa-exec`, and the exec that performs the profile transition is
authorised by that transition, not by the exec rules of the profile being
entered. Everything exec'd afterwards is mediated normally -- and the confined
profile permits execution only under `@{CLICK_DIR}/@{APP_PKGNAME}/@{APP_VERSION}/**`,
so nothing in `/usr/bin`, shell included.

The practical rule: **a click may ship and run its own ELF binaries, but not
its own shell scripts**, beyond the single entry point named in the .desktop
file. A `#!/bin/sh` helper looks fine in every host test and fails only on the
device.

That caught `ocbridge-auth`, the `auth-user-pass-verify` helper: openvpn would
have completed the TLS handshake and then rejected the client with a plain
authentication failure, with nothing anywhere to say the interpreter could not
be executed. It is now `ocbridge --auth`, a mode of the Go binary, invoked as

    auth-user-pass-verify "<ocbridge> --auth" via-file

(openvpn execve()s that argv directly -- no shell.) `config_test.go` asserts the
config never references a shell helper again.

## CORRECTION: a click's own shell script runs; the utilities it calls do not

The previous section's reasoning was wrong in its detail, and the device said
so. The denial was not the shell:

    apparmor="DENIED" operation="exec" name="/usr/bin/sed" comm="ocbridge-auth"

`ocbridge-auth` (a `#!/bin/sh` script inside the click) **did** execute -- the
shebang interpreter is reached through the exec of a file inside the package
tree, which the profile allows -- and then died trying to run `sed`. So the
accurate rule is:

- A click **may** exec its own files, including shell scripts.
- A click **may not** exec anything in `/usr/bin`, so a script may use only
  shell built-ins. `sed`, `grep`, `cut`, `dirname` are all unavailable.
- openconnect's `execl("/bin/sh", ...)` was denied because that is an explicit
  exec of a path *outside* the package tree, which is a different thing from
  reaching dash via a shebang.

The fix stands either way -- `ocbridge --auth` in the Go binary depends on no
external utility at all -- but the reason recorded for it must be right, or the
next person will draw the wrong boundary.

This also explains a confusing earlier observation: a run started from
`adb shell` authenticated successfully while the confined one did not. `adb
shell` is unconfined, so its `sed` worked. **Never conclude anything about
confinement from a process started over adb.**

## Lomiri suspends the WHOLE process tree, and start_new_session does not help

The decisive bug. With the tunnel up and the client connecting, the listening
socket showed `LISTEN 2` -- two connections queued and never accepted -- and
the client reported `TLS key negotiation failed to occur within 60 seconds`.
The reason:

    53005 T qmlscene        <- the app
    53024 Ts openconnect    <- started with start_new_session=True
    53025 Tl ocbridge
    53030 T  openvpn        <- cannot accept()

all in state `T`, `wchan=do_signal_stop`. Lomiri froze the entire tree, not
just the app's process group, so **`start_new_session=True` does not protect
background work** -- contrary to what was assumed from Wireguard_UT (whose
privileged helper runs as root, outside the app's scope).

This is fatal for this architecture specifically, because the user *must* be in
the Settings app to switch the VPN on: the app is guaranteed to be in the
background at the one moment its server has to answer.

The supported fix is Lomiri's own lifecycle exemption list:

    gsettings get com.canonical.qtmir lifecycle-exempt-appids
    ['music.ubports', 'lomiri-system-settings', 'terminal.ubports',
     'ut-tweak-tool.sverzegnassi', ...]

Adding `ocvpn.franzthiemann` to it is exactly what UT Tweak Tool's "Lifecycle
exceptions" page does, and is how Terminal stays alive. A confined app cannot
set this itself -- the profile carries `deny /run/user/[0-9]*/dconf/user rw` --
so it is a documented one-time user step, now the **first** item on the setup
page.

Diagnostic worth reusing: `awk '{print $3}' /proc/<pid>/stat` (or `ps -o stat`)
distinguishes a suspended process from a hung one instantly. A `T` state with
`do_signal_stop` in wchan means the shell froze it, not that your code blocked.

## A pushed route can never be a bypass — it is the opposite of one

The plan's routing-loop fix was wrong, and only the device could show it.

The reasoning was: openconnect's socket to the Cisco gateway must stay off the
tunnel it carries, and openvpn's `net_gateway` keyword resolves to the real
default gateway even under `--route-noexec` (verified, twice, in source and on
the rig). So push `route <gateway> 255.255.255.255 net_gateway`.

What actually lands on the device:

    139.18.110.147 via <lan-gateway> dev tun0

**NetworkManager applies every route a VPN pushes to the VPN device.** The
next-hop is honoured, the device is not. So the rule routes the gateway *into*
the tunnel — precisely the loop it was meant to prevent. Symptoms:
`bytes_out` climbing, **`bytes_in` stuck at 0**, and no network at all.

The rig could never catch this: there the client applies routes itself, with no
NetworkManager to rewrite the device.

Two consequences:

- **Push no route for the gateway.** The transport stays off the tunnel by not
  being routed onto it, which holds as long as there is no default route via
  the VPN.
- **Full-tunnel mode cannot work in this architecture.** NetworkManager's own
  protection for a VPN's transport uses the VPN's remote address (`trusted_ip`),
  which here is `127.0.0.1` — it dutifully adds `127.0.0.1 via <gw> dev wlan0`,
  protecting nothing. A VPN plugin cannot install a route on the physical
  device, and binding openconnect's socket to an interface needs CAP_NET_RAW.
  So "Only use connection for VPN resources" is **required**, not a preference,
  and the split routes the gateway sends are what goes through the tunnel.

For Uni Leipzig that means the `1-Standard-Uni` group works and
`2-Spezial-Alles` (everything through the VPN) does not.

### Diagnosing this class of problem

`ip route get <addr>` is the direct question, and answers it per-destination:

    ip route get 139.18.110.147   -> via ... dev tun0   (wrong: transport in tunnel)
    ip route get 1.1.1.1          -> via ... dev tun0   (full tunnel)

Paired with `bytes_in`/`bytes_out` from status.json, an inbound count stuck at
zero while outbound climbs is the signature of the transport being routed into
its own tunnel.

## The gateway sits inside its own split route

Removing the bad bypass push fixed general internet access but broke the VPN
itself, and the reason is worth stating plainly:

    SYN-SENT  <tunnel-address>:47530 -> 139.18.110.147:443
    DTLS Dead Peer Detection detected dead peer!
    Failed to reconnect to host vpn.uni-leipzig.de: Connection timed out

openconnect was reconnecting **from the tunnel's own address**. Uni Leipzig
pushes `139.18.0.0/16` as a split route, and its gateway lives at
`139.18.110.147` — **inside that route**. So the transport is swallowed by the
tunnel even in split-tunnel mode, with no default route involved at all.

This is likely to be common rather than peculiar: a university or company
naturally hands out routes for the same address space its VPN concentrator
sits in.

Since a pushed route cannot be a bypass (NetworkManager binds it to the VPN
device), the hole has to be left in the routes that *are* pushed.
`routes.go:ExcludeHost` subtracts the gateway's /32 from any block containing
it by repeated halving — a /16 minus a /32 becomes 16 blocks — so
`172.18.0.0/16` is carried intact while `139.18.0.0/16` arrives as 16 pieces
with the gateway's address left outside the tunnel.

Symptom to recognise: `bytes_out` climbing while **`bytes_in` stays exactly 0**,
plus `ss -tnp` showing openconnect's socket in `SYN-SENT` with a *tunnel* source
address. `ip route get <gateway>` names the culprit immediately.

## A dead tunnel leaves NetworkManager holding the routes

Once the tunnel dies, the VPN stays `activated` in NetworkManager and keeps its
routes — including the one pointing the gateway into the now-dead tunnel. The
next connection attempt therefore cannot reach the gateway at all, and fails
with a bare timeout that looks like a network problem.

The app cannot clear this: `nmcli connection down` is refused with *"Not
authorized to deactivate connections"* for the phablet user, and a confined app
may not touch NetworkManager at all. So the order matters and has to be taught:

1. VPN **off** in Settings
2. connect in the app
3. VPN **on** in Settings

`backend.py` now recognises an unreachable gateway and says this outright,
rather than echoing openconnect's timeout.
