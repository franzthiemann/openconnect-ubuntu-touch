package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Route is one split-include route the Cisco gateway asked us to carry.
type Route struct {
	Network net.IP
	Mask    net.IP
}

// Params is the tunnel description openconnect hands to its --script-tun child
// through the environment. The names are vpnc-script's; openconnect exports the
// same set in script-tun mode (verified live against ocserv, see
// tasks/lessons.md), plus VPNFD for the socketpair.
type Params struct {
	VPNFD   int
	Gateway net.IP // VPNGATEWAY: the real gateway we are connected to
	Address net.IP // INTERNAL_IP4_ADDRESS: the address the gateway assigned us
	Netmask net.IP
	NetAddr net.IP
	MTU     int
	DNS     []net.IP
	Domain  string
	Splits  []Route
	Reason  string
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func parseIP4(k string) (net.IP, error) {
	v := env(k)
	if v == "" {
		return nil, fmt.Errorf("%s is not set", k)
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return nil, fmt.Errorf("%s=%q is not an IP address", k, v)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("%s=%q is not IPv4", k, v)
	}
	return ip4, nil
}

// ParseParams reads and validates the script-tun environment.
func ParseParams() (*Params, error) {
	p := &Params{Reason: env("reason"), Domain: env("CISCO_DEF_DOMAIN")}

	fdStr := env("VPNFD")
	if fdStr == "" {
		return nil, fmt.Errorf("VPNFD is not set -- ocbridge must be run by " +
			"openconnect --script-tun, not directly")
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil || fd < 0 {
		return nil, fmt.Errorf("VPNFD=%q is not a descriptor number", fdStr)
	}
	p.VPNFD = fd

	if p.Address, err = parseIP4("INTERNAL_IP4_ADDRESS"); err != nil {
		return nil, err
	}
	if p.Netmask, err = parseIP4("INTERNAL_IP4_NETMASK"); err != nil {
		return nil, err
	}
	if p.NetAddr, err = parseIP4("INTERNAL_IP4_NETADDR"); err != nil {
		// Not every gateway sends it; derive it instead of failing.
		p.NetAddr = p.Address.Mask(net.IPMask(p.Netmask))
	}

	// VPNGATEWAY may be IPv6 (our ocserv rig reports ::1 over localhost). That
	// is not an error -- it only means we cannot add the IPv4 bypass route, so
	// leave Gateway nil and let the caller decide.
	if gw := net.ParseIP(env("VPNGATEWAY")); gw != nil {
		p.Gateway = gw.To4()
	}

	p.MTU = 1400
	if v := env("INTERNAL_IP4_MTU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 576 && n <= 65535 {
			p.MTU = n
		}
	}

	// INTERNAL_IP4_DNS and _NBNS hold up to three addresses in ONE variable,
	// space separated -- not an indexed family like the split routes.
	for _, f := range strings.Fields(env("INTERNAL_IP4_DNS")) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			p.DNS = append(p.DNS, ip.To4())
		}
	}

	// CISCO_SPLIT_INC is a count; the routes are CISCO_SPLIT_INC_%d_ADDR/_MASK.
	// Both dotted MASK and MASKLEN are exported, and openvpn wants the dotted
	// form, so no conversion is needed.
	n, _ := strconv.Atoi(env("CISCO_SPLIT_INC"))
	for i := 0; i < n; i++ {
		addr := net.ParseIP(env(fmt.Sprintf("CISCO_SPLIT_INC_%d_ADDR", i)))
		mask := net.ParseIP(env(fmt.Sprintf("CISCO_SPLIT_INC_%d_MASK", i)))
		if addr == nil || mask == nil || addr.To4() == nil || mask.To4() == nil {
			continue
		}
		p.Splits = append(p.Splits, Route{Network: addr.To4(), Mask: mask.To4()})
	}
	return p, nil
}

// ServerIP picks the address the OpenVPN server takes on its side of the
// loopback tunnel. The client must receive the gateway-assigned address
// verbatim, otherwise the Cisco side drops its packets and we would need NAT,
// so the server takes the first other host address in the same subnet.
func (p *Params) ServerIP() (net.IP, error) {
	ones, bits := net.IPMask(p.Netmask).Size()
	if bits != 32 {
		return nil, fmt.Errorf("netmask %v is not a valid IPv4 mask", p.Netmask)
	}
	if ones > 29 {
		// openvpn refuses an ifconfig-pool smaller than two addresses, even
		// though a ccd entry pins the client's address anyway, so a /30 does
		// not leave enough room once the local endpoint takes one. Diagnose it
		// here rather than emitting a config openvpn rejects obscurely with
		// "IPv4 pool size is too small".
		return nil, fmt.Errorf("gateway assigned a /%d; ocbridge needs a /29 or "+
			"larger to place the local OpenVPN endpoint (address-rewriting "+
			"fallback is not implemented)", ones)
	}
	base := p.NetAddr.Mask(net.IPMask(p.Netmask)).To4()
	cand := make(net.IP, 4)
	for _, off := range []byte{1, 2} {
		copy(cand, base)
		cand[3] += off
		if !cand.Equal(p.Address) {
			out := make(net.IP, 4)
			copy(out, cand)
			return out, nil
		}
	}
	return nil, fmt.Errorf("cannot place the local endpoint next to %v/%d", p.Address, ones)
}

func ip2u32(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func u32toIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}

// PoolRange returns the ifconfig-pool bounds for the local OpenVPN server.
//
// The pool is not what decides the client's address -- the ccd DEFAULT entry
// pins that to the gateway-assigned one. But openvpn validates the pool at
// startup and dies with "IPv4 pool size is too small (1), must be at least 2"
// if it holds fewer than two addresses, so hand it the whole usable remainder
// of the subnet above the local endpoint instead of a single slot.
func (p *Params) PoolRange(serverIP net.IP) (net.IP, net.IP, error) {
	ones, _ := net.IPMask(p.Netmask).Size()
	base := ip2u32(p.NetAddr.Mask(net.IPMask(p.Netmask)).To4())
	broadcast := base | ^(^uint32(0) << uint(32-ones))

	start := ip2u32(serverIP) + 1
	end := broadcast - 1
	if end < start || end-start+1 < 2 {
		return nil, nil, fmt.Errorf("no room for an address pool in a /%d", ones)
	}
	return u32toIP(start), u32toIP(end), nil
}
