package main

import (
	"net"
	"strings"
	"testing"
)

// rigEnv is the environment openconnect actually exported in the script-tun
// probe against ocserv, plus a realistic IPv4 gateway. Captured, not invented.
func rigEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	base := map[string]string{
		"VPNFD":                   "9",
		"VPNGATEWAY":              "139.18.110.147",
		"reason":                  "connect",
		"INTERNAL_IP4_ADDRESS":    "10.99.0.121",
		"INTERNAL_IP4_NETMASK":    "255.255.255.0",
		"INTERNAL_IP4_NETMASKLEN": "24",
		"INTERNAL_IP4_NETADDR":    "10.99.0.0",
		"INTERNAL_IP4_MTU":        "1434",
		"INTERNAL_IP4_DNS":        "10.99.0.53 10.99.0.54",
		"CISCO_DEF_DOMAIN":        "rig.test",
		"CISCO_SPLIT_INC":         "2",
		"CISCO_SPLIT_INC_0_ADDR":  "172.31.5.0",
		"CISCO_SPLIT_INC_0_MASK":  "255.255.255.0",
		"CISCO_SPLIT_INC_1_ADDR":  "10.200.0.0",
		"CISCO_SPLIT_INC_1_MASK":  "255.255.0.0",
	}
	for k, v := range overrides {
		if v == "" {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

func TestParseParamsFromRigEnvironment(t *testing.T) {
	rigEnv(t, nil)
	p, err := ParseParams()
	if err != nil {
		t.Fatalf("ParseParams: %v", err)
	}
	if p.VPNFD != 9 {
		t.Errorf("VPNFD = %d, want 9", p.VPNFD)
	}
	if !p.Address.Equal(net.ParseIP("10.99.0.121")) {
		t.Errorf("Address = %v", p.Address)
	}
	if p.MTU != 1434 {
		t.Errorf("MTU = %d, want 1434", p.MTU)
	}
	// INTERNAL_IP4_DNS packs several addresses into ONE space-separated
	// variable; it is not an indexed family like the split routes.
	if len(p.DNS) != 2 {
		t.Errorf("DNS = %v, want 2 entries", p.DNS)
	}
	if len(p.Splits) != 2 || !p.Splits[1].Network.Equal(net.ParseIP("10.200.0.0")) {
		t.Errorf("Splits = %+v", p.Splits)
	}
	if p.Domain != "rig.test" {
		t.Errorf("Domain = %q", p.Domain)
	}
}

func TestParseParamsRequiresScriptTunEnvironment(t *testing.T) {
	rigEnv(t, map[string]string{"VPNFD": ""})
	if _, err := ParseParams(); err == nil ||
		!strings.Contains(err.Error(), "--script-tun") {
		t.Fatalf("want a message pointing at --script-tun, got %v", err)
	}
}

// The rig reports VPNGATEWAY=::1 over loopback. That must not be fatal: it only
// means the IPv4 bypass route cannot be built.
func TestParseParamsToleratesIPv6Gateway(t *testing.T) {
	rigEnv(t, map[string]string{"VPNGATEWAY": "::1"})
	p, err := ParseParams()
	if err != nil {
		t.Fatalf("ParseParams: %v", err)
	}
	if p.Gateway != nil {
		t.Errorf("Gateway = %v, want nil for an IPv6 gateway", p.Gateway)
	}
}

func TestParseParamsDerivesNetAddrWhenAbsent(t *testing.T) {
	rigEnv(t, map[string]string{"INTERNAL_IP4_NETADDR": ""})
	p, err := ParseParams()
	if err != nil {
		t.Fatalf("ParseParams: %v", err)
	}
	if !p.NetAddr.Equal(net.ParseIP("10.99.0.0")) {
		t.Errorf("NetAddr = %v, want 10.99.0.0", p.NetAddr)
	}
}

func TestServerIPAvoidsTheClientAddress(t *testing.T) {
	for _, tc := range []struct{ client, want string }{
		{"10.99.0.121", "10.99.0.1"},
		{"10.99.0.1", "10.99.0.2"}, // must not collide with the assigned address
	} {
		rigEnv(t, map[string]string{"INTERNAL_IP4_ADDRESS": tc.client})
		p, err := ParseParams()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.ServerIP()
		if err != nil {
			t.Fatalf("ServerIP for client %s: %v", tc.client, err)
		}
		if got.String() != tc.want {
			t.Errorf("client %s: ServerIP = %v, want %s", tc.client, got, tc.want)
		}
		if got.Equal(p.Address) {
			t.Errorf("client %s: server took the client's address", tc.client)
		}
	}
}

// A /32 assignment leaves nowhere to put the local endpoint, and the no-NAT
// scheme cannot work. Fail with something a human can act on.
func TestServerIPRejectsTooSmallAPrefix(t *testing.T) {
	rigEnv(t, map[string]string{"INTERNAL_IP4_NETMASK": "255.255.255.255"})
	p, err := ParseParams()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ServerIP()
	if err == nil || !strings.Contains(err.Error(), "/32") {
		t.Fatalf("want a diagnosable /32 error, got %v", err)
	}
}
