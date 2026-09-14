package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildFromRig(t *testing.T) string {
	t.Helper()
	rigEnv(t, nil)
	p, err := ParseParams()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := p.ServerIP()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildServerConfig(p, Paths{
		CACert: "/d/ca.crt", ServerCert: "/d/server.crt", ServerKey: "/d/server.key",
		CCDDir: "/d/ccd", AuthHelper: "/pkg/bin/ocbridge-auth", CredsFile: "/d/vpn-creds",
	}, srv, 3, 1194, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestServerConfigCarriesTheLoadBearingDirectives(t *testing.T) {
	cfg := buildFromRig(t)
	for _, want := range []string{
		"dev-node fd:3",   // our openvpn patch; no tun device
		"ifconfig-noexec", // nothing to configure on this side
		"route-noexec",    //   "
		"local 127.0.0.1", // loopback only
		"verify-client-cert none",
		`auth-user-pass-verify "/pkg/bin/ocbridge-auth --auth" via-file`,
		"setenv OCBRIDGE_CREDS /d/vpn-creds",
		"tun-mtu 1434", // match the Cisco MTU
		`push "tun-mtu 1434"`,
		"ifconfig 10.99.0.1 255.255.255.0",
		"ifconfig-pool 10.99.0.2 10.99.0.254 255.255.255.0", // >= 2 or openvpn dies
		`push "route 172.31.5.0 255.255.255.0"`,
		`push "route 10.200.0.0 255.255.0.0"`,
		`push "dhcp-option DNS 10.99.0.53"`,
		`push "dhcp-option DNS 10.99.0.54"`,
		`push "dhcp-option DOMAIN rig.test"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config is missing %q\n---\n%s", want, cfg)
		}
	}
}

// redirect-gateway is a no-op under NetworkManager's --route-noexec (verified in
// tests/rig/ovpn_route_env_test.py). Pushing it would be cargo cult, and would
// mislead the next reader into thinking it controls the default route.
func TestServerConfigDoesNotPushRedirectGateway(t *testing.T) {
	if cfg := buildFromRig(t); strings.Contains(cfg, "redirect-gateway") {
		t.Error("config pushes redirect-gateway; it does nothing under --route-noexec")
	}
}

// ccd-exclusive ignores the DEFAULT file and demands one named after the common
// name -- the exact mistake that made the first fd spike fail.
func TestServerConfigDoesNotUseCcdExclusive(t *testing.T) {
	if cfg := buildFromRig(t); strings.Contains(cfg, "ccd-exclusive") {
		t.Error("ccd-exclusive is set, which makes the DEFAULT ccd file be ignored")
	}
}

// Pushing a route for the gateway is worse than useless: NetworkManager binds
// pushed routes to the VPN device, so it routes openconnect's own transport
// into the tunnel it is carrying. Verified on device -- bytes_in stayed at 0.
func TestTheGatewayIsNeverPushedAsARoute(t *testing.T) {
	cfg := buildFromRig(t)
	if strings.Contains(cfg, "net_gateway") {
		t.Error("config pushes a net_gateway route; NetworkManager will bind it " +
			"to the VPN device and loop the transport into the tunnel")
	}
	if strings.Contains(cfg, "198.18.110.147") {
		t.Error("config pushes a route for the gateway address")
	}
}

func TestCCDPushesTheGatewayAssignedAddress(t *testing.T) {
	rigEnv(t, nil)
	p, _ := ParseParams()
	if got := BuildCCD(p); !strings.Contains(got, "ifconfig-push 10.99.0.121 255.255.255.0") {
		t.Errorf("ccd = %q", got)
	}
}

func TestEnsurePKIIsIdempotentAndChains(t *testing.T) {
	dir := t.TempDir()
	p1, err := EnsurePKI(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p1.CACert)
	if err != nil {
		t.Fatal(err)
	}
	// Regenerating the CA would silently invalidate the ca.crt the user already
	// picked by hand in the Settings VPN editor.
	if _, err := EnsurePKI(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p1.CACert)
	if string(before) != string(after) {
		t.Fatal("EnsurePKI regenerated an existing CA")
	}

	parse := func(path string) *x509.Certificate {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			t.Fatalf("%s is not PEM", path)
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ca, srv := parse(p1.CACert), parse(p1.ServerCert)
	if !ca.IsCA {
		t.Error("CA certificate is not marked as a CA")
	}
	if err := srv.CheckSignatureFrom(ca); err != nil {
		t.Errorf("server certificate does not chain to the CA: %v", err)
	}
	// --remote-cert-tls server rejects a server certificate without this EKU.
	found := false
	for _, e := range srv.ExtKeyUsage {
		if e == x509.ExtKeyUsageServerAuth {
			found = true
		}
	}
	if !found {
		t.Error("server certificate lacks the serverAuth EKU")
	}
	for _, f := range []string{filepath.Join(dir, "ca.key"), p1.ServerKey} {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", f, st.Mode().Perm())
		}
	}
}

func TestEnsureCredsAreStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-creds")
	u1, p1, err := EnsureCreds(path)
	if err != nil {
		t.Fatal(err)
	}
	u2, p2, err := EnsureCreds(path)
	if err != nil {
		t.Fatal(err)
	}
	// The user types these into Settings once; they must not drift.
	if u1 != u2 || p1 != p2 {
		t.Fatalf("credentials changed between calls: %q/%q then %q/%q", u1, p1, u2, p2)
	}
	if len(p1) < 12 {
		t.Errorf("password %q is too short", p1)
	}
	// The alphabet excludes characters that are ambiguous on a phone keyboard.
	if strings.ContainsAny(u1+p1, "0O1lI") {
		t.Errorf("credentials contain ambiguous characters: %q %q", u1, p1)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("creds file mode %v, want 0600", st.Mode().Perm())
	}
}

// openvpn validates the pool at startup and refuses one smaller than two
// addresses, regardless of the ccd entry that actually pins the address.
func TestPoolIsNeverSmallerThanTwoAddresses(t *testing.T) {
	for _, mask := range []string{"255.255.255.0", "255.255.255.248"} {
		rigEnv(t, map[string]string{"INTERNAL_IP4_NETMASK": mask})
		p, err := ParseParams()
		if err != nil {
			t.Fatal(err)
		}
		srv, err := p.ServerIP()
		if err != nil {
			t.Fatalf("mask %s: %v", mask, err)
		}
		start, end, err := p.PoolRange(srv)
		if err != nil {
			t.Fatalf("mask %s: %v", mask, err)
		}
		if n := ip2u32(end) - ip2u32(start) + 1; n < 2 {
			t.Errorf("mask %s: pool holds %d addresses", mask, n)
		}
		if ip2u32(srv) >= ip2u32(start) && ip2u32(srv) <= ip2u32(end) {
			t.Errorf("mask %s: pool %v-%v contains the local endpoint %v", mask, start, end, srv)
		}
	}
}

// A /30 cannot host both the local endpoint and a two-address pool.
func TestTooSmallSubnetIsRejectedEarly(t *testing.T) {
	rigEnv(t, map[string]string{"INTERNAL_IP4_NETMASK": "255.255.255.252"})
	p, _ := ParseParams()
	if _, err := p.ServerIP(); err == nil || !strings.Contains(err.Error(), "/29") {
		t.Fatalf("want an actionable /29 message, got %v", err)
	}
}

// A protocol mismatch is invisible server-side -- it just never sees a packet
// -- so pin both forms down.
func TestProtocolIsSelectable(t *testing.T) {
	rigEnv(t, nil)
	p, _ := ParseParams()
	srv, _ := p.ServerIP()
	for proto, want := range map[string]string{"tcp": "proto tcp-server", "udp": "proto udp"} {
		cfg, err := BuildServerConfig(p, Paths{}, srv, 3, 1194, proto)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cfg, want) {
			t.Errorf("proto %q: config lacks %q", proto, want)
		}
	}
}

func TestResolveProtoDefaultsToTCP(t *testing.T) {
	t.Setenv("OCBRIDGE_PROTO", "")
	if got := resolveProto(""); got != "tcp" {
		t.Errorf("default proto = %q, want tcp (the UT VPN editor writes proto-tcp=yes)", got)
	}
	if got := resolveProto("udp"); got != "udp" {
		t.Errorf("explicit udp = %q", got)
	}
	t.Setenv("OCBRIDGE_PROTO", "udp")
	if got := resolveProto(""); got != "udp" {
		t.Errorf("env udp = %q", got)
	}
}

// The password check must not go through a shell script. A confined click may
// not execute /bin/sh, and a #!/bin/sh helper would fail as a bare
// authentication failure with nothing in any log to explain it.
func TestAuthHelperIsTheBinaryItselfNotAScript(t *testing.T) {
	cfg := buildFromRig(t)
	if !strings.Contains(cfg, `--auth" via-file`) {
		t.Error("auth-user-pass-verify does not invoke ocbridge with --auth")
	}
	for _, forbidden := range []string{"/bin/sh", "ocbridge-auth via-file"} {
		if strings.Contains(cfg, forbidden) {
			t.Errorf("config still references %q", forbidden)
		}
	}
}
