# Patched OpenVPN

The click ships a modified `openvpn` binary. OpenVPN is **GPLv2**, so this
directory is the corresponding source offer:

- `0001-dev-node-fd.patch` — the modification, against openvpn 2.6.19.
- `../../tools/prebuild.sh` — the exact build recipe, including the pinned
  upstream tarball URL and its SHA-256.

Unmodified upstream source:
<https://swupdate.openvpn.org/community/releases/openvpn-2.6.19.tar.gz>

## What the patch does

It adds `--dev-node fd:N`, which makes openvpn adopt an already-open descriptor
as its tun device instead of opening `/dev/net/tun` and issuing `TUNSETIFF`.
That is what lets it run as an ordinary confined user with no `CAP_NET_ADMIN`
and no tun device, exchanging raw IP packets over an inherited socket.

The patch is small because Linux openvpn already opens tun with `IFF_NO_PI`, so
`read_tun()`/`write_tun()` are a bare `read()`/`write()` of unprefixed IP
packets either way — byte-identical to what openconnect's `--script-tun`
socketpair carries.

Callers must also pass `--ifconfig-noexec` and `--route-noexec`: there is no
interface to configure. Build with `--disable-dco`, because with kernel offload
openvpn never touches that descriptor at all.
