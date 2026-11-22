package mdns

import "net"

// advertisable says whether ip is a home network's address. IPv4: private
// or link-local. IPv6: link-local or unique local (ULA). Never a public
// one: that's a WAN, or someone else's network.
func advertisable(ip net.IP) bool {
	if ip.To4() != nil {
		return ip.IsPrivate() || ip.IsLinkLocalUnicast() || anyAddress
	}
	return ip.IsLinkLocalUnicast() || ip.IsPrivate()
}

// anyAddress is for tests on a real network, which may not be a private
// one (a container's, say).
var anyAddress bool

// An iface is an interface that's up and does multicast, with its
// addresses.
type iface struct {
	index int
	name  string
	addrs []*net.IPNet
}

func interfaces() []iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []iface
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		i := iface{index: ifi.Index, name: ifi.Name}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				i.addrs = append(i.addrs, ipn)
			}
		}
		out = append(out, i)
	}
	return out
}

// maxAAAA is the most IPv6 addresses given for an interface: its
// link-local one and a unique local or two is all a client needs.
const maxAAAA = 3

// list is the networks to advertise on: for each interface, one for each
// advertisable IPv4 address that allow accepts (nil: all of them), if v4;
// and one for IPv6, if v6 and the interface has a link-local address
// (which is what mDNS over IPv6 is sent from) that allow accepts.
func list(ifs []iface, allow func(ifname string, ip net.IP) bool, v4, v6 bool) []*network {
	var out []*network
	for _, ifi := range ifs {
		var fours []*net.IPNet
		var sixes []net.IP
		linkLocal := false
		for _, a := range ifi.addrs {
			if !advertisable(a.IP) || (allow != nil && !allow(ifi.name, a.IP)) {
				continue
			}
			switch {
			case a.IP.To4() != nil:
				fours = append(fours, a)
			case a.IP.IsLinkLocalUnicast() && !linkLocal:
				sixes = append([]net.IP{a.IP}, sixes...) // first: it's what packets go from
				linkLocal = true
			default:
				sixes = append(sixes, a.IP)
			}
		}
		sixes = sixes[:min(len(sixes), maxAAAA)]
		var a net.IP
		if len(fours) > 0 {
			a = fours[0].IP.To4()
		}
		if v4 {
			for _, f := range fours {
				out = append(out, &network{ifindex: ifi.index, name: ifi.name, ip: f.IP.To4(), net: f, a: f.IP.To4(), aaaa: sixes})
			}
		}
		if v6 && linkLocal {
			out = append(out, &network{ifindex: ifi.index, name: ifi.name, v6: true, ip: sixes[0], a: a, aaaa: sixes})
		}
	}
	return out
}

// networkFor finds the IPv4 network a query came from, so the answer
// gives the address the asker can reach. None: the query isn't from a
// network the service is advertised on, and gets no answer.
func networkFor(nets []*network, from net.IP) (*network, bool) {
	for _, n := range nets {
		if !n.v6 && n.net.Contains(from) {
			return n, true
		}
	}
	return nil, false
}

// networkFor6 finds the IPv6 network a query came from: the one on the
// interface it arrived on (ifindex, where the system says; else the zone
// of the sender's link-local address). Only from an address of the kind
// that's advertised: one from further off isn't answered.
func networkFor6(nets []*network, from *net.UDPAddr, ifindex int) (*network, bool) {
	if !advertisable(from.IP) || from.IP.To4() != nil {
		return nil, false
	}
	for _, n := range nets {
		if n.v6 && (ifindex != 0 && n.ifindex == ifindex || ifindex == 0 && n.name == from.Zone) {
			return n, true
		}
	}
	return nil, false
}
