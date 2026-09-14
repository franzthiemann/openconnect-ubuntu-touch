package main

import (
	"math/rand"
	"net"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The real case: Uni Leipzig pushes 139.18.0.0/16 and the gateway it is
// reached through lives at 139.18.110.147, inside it.
func TestExcludeHostCoversEverythingButTheHost(t *testing.T) {
	block := mustCIDR(t, "139.18.0.0/16")
	host := net.ParseIP("139.18.110.147")

	pieces := ExcludeHost(block, host)
	if len(pieces) != 16 {
		t.Errorf("got %d pieces, want 16 for a /16 minus a /32", len(pieces))
	}

	covered := func(ip net.IP) bool {
		for _, p := range pieces {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}

	if covered(host) {
		t.Fatal("the gateway is still inside a pushed route -- the transport " +
			"would be routed into the tunnel carrying it")
	}

	// Boundaries, the neighbours of the hole, and a random sample: everything
	// in the block except the host itself must still be routed.
	for _, s := range []string{"139.18.0.0", "139.18.0.1", "139.18.255.255",
		"139.18.110.146", "139.18.110.148", "139.18.110.0", "139.18.111.0"} {
		if ip := net.ParseIP(s); !covered(ip) {
			t.Errorf("%s fell out of the routes", s)
		}
	}
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 4000; i++ {
		ip := u32toIP(ip2u32(net.ParseIP("139.18.0.0").To4()) + uint32(rnd.Intn(1<<16)))
		if ip.Equal(host) {
			continue
		}
		if !covered(ip) {
			t.Fatalf("%v fell out of the routes", ip)
		}
	}

	// The pieces must not overlap each other.
	for i := 0; i < len(pieces); i++ {
		for j := i + 1; j < len(pieces); j++ {
			if pieces[i].Contains(pieces[j].IP) || pieces[j].Contains(pieces[i].IP) {
				t.Errorf("pieces %v and %v overlap", pieces[i], pieces[j])
			}
		}
	}
}

func TestExcludeHostLeavesUnrelatedBlocksAlone(t *testing.T) {
	block := mustCIDR(t, "172.18.0.0/16")
	pieces := ExcludeHost(block, net.ParseIP("139.18.110.147"))
	if len(pieces) != 1 || pieces[0].String() != block.String() {
		t.Errorf("got %v, want the block unchanged", pieces)
	}
}

func TestExcludeHostOfAHostRouteLeavesNothing(t *testing.T) {
	block := mustCIDR(t, "139.18.110.147/32")
	if pieces := ExcludeHost(block, net.ParseIP("139.18.110.147")); len(pieces) != 0 {
		t.Errorf("got %v, want nothing", pieces)
	}
}

// End to end on the environment the real gateway sends.
func TestSplitRoutesPunchOutTheGateway(t *testing.T) {
	rigEnv(t, map[string]string{
		"VPNGATEWAY":             "139.18.110.147",
		"CISCO_SPLIT_INC":        "2",
		"CISCO_SPLIT_INC_0_ADDR": "139.18.0.0",
		"CISCO_SPLIT_INC_0_MASK": "255.255.0.0",
		"CISCO_SPLIT_INC_1_ADDR": "172.18.0.0",
		"CISCO_SPLIT_INC_1_MASK": "255.255.0.0",
	})
	p, err := ParseParams()
	if err != nil {
		t.Fatal(err)
	}
	routes := p.SplitRoutes()

	gw := net.ParseIP("139.18.110.147")
	var unrelated int
	for _, r := range routes {
		n := &net.IPNet{IP: r.Network.To4(), Mask: net.IPMask(r.Mask.To4())}
		if n.Contains(gw) {
			t.Errorf("route %v still contains the gateway", n)
		}
		if n.Contains(net.ParseIP("172.18.139.176")) {
			unrelated++
		}
	}
	// The workstation's network must survive untouched.
	if unrelated != 1 {
		t.Errorf("172.18.0.0/16 was not carried through intact (%d matches)", unrelated)
	}
	if len(routes) != 17 { // 16 pieces of 139.18/16 + 172.18/16 whole
		t.Errorf("got %d routes, want 17", len(routes))
	}
}
