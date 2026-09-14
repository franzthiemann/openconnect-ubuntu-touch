package main

import (
	"net"
)

// ExcludeHost returns the set of CIDR blocks covering block minus host.
//
// Used to punch the Cisco gateway's own address out of the split routes the
// gateway sends. Uni Leipzig, for example, pushes 139.18.0.0/16 and lives at
// 139.18.110.147 -- so carrying that route verbatim routes openconnect's own
// transport into the tunnel it is carrying, and the session dies the moment it
// has to reconnect.
//
// A bypass route cannot be pushed instead: NetworkManager binds every pushed
// route to the VPN device, which is the opposite of what a bypass needs. The
// hole has to be left in the routes we do push.
//
// The result is the usual binary split: repeatedly halve the block, keep the
// half without the host, and recurse into the half with it. A /16 minus a /32
// yields 16 blocks.
func ExcludeHost(block *net.IPNet, host net.IP) []*net.IPNet {
	host = host.To4()
	blockIP := block.IP.To4()
	if host == nil || blockIP == nil || !block.Contains(host) {
		return []*net.IPNet{block}
	}

	ones, bits := block.Mask.Size()
	if bits != 32 {
		return []*net.IPNet{block}
	}
	if ones == 32 {
		// The block *is* the host: nothing left to route.
		return nil
	}

	var out []*net.IPNet
	cur := &net.IPNet{IP: blockIP, Mask: block.Mask}
	for prefix := ones; prefix < 32; prefix++ {
		lo, hi := halves(cur, prefix+1)
		// Keep whichever half does not hold the host; continue splitting the
		// one that does.
		keep, descend := hi, lo
		if hi.Contains(host) {
			keep, descend = lo, hi
		}
		out = append(out, keep)
		cur = descend
	}
	return out
}

// halves splits a block into its two sub-blocks at the given prefix length.
func halves(block *net.IPNet, prefix int) (lo, hi *net.IPNet) {
	mask := net.CIDRMask(prefix, 32)
	base := ip2u32(block.IP.To4())
	lo = &net.IPNet{IP: u32toIP(base), Mask: mask}
	hi = &net.IPNet{IP: u32toIP(base | 1<<uint(32-prefix)), Mask: mask}
	return lo, hi
}

// SplitRoutes returns the routes to push, with the gateway's address excluded
// from any that would otherwise swallow it.
func (p *Params) SplitRoutes() []Route {
	var out []Route
	for _, r := range p.Splits {
		block := &net.IPNet{IP: r.Network.To4(), Mask: net.IPMask(r.Mask.To4())}
		if p.Gateway == nil || !block.Contains(p.Gateway) {
			out = append(out, r)
			continue
		}
		for _, piece := range ExcludeHost(block, p.Gateway) {
			ones, _ := piece.Mask.Size()
			out = append(out, Route{
				Network: piece.IP.To4(),
				Mask:    net.IP(net.CIDRMask(ones, 32)).To4(),
			})
		}
	}
	return out
}
