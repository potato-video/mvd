package mdns

import (
	"net"
	"strings"
	"testing"
	"time"
)

func ips(s ...string) (out []net.IP) {
	for _, x := range s {
		out = append(out, net.ParseIP(x))
	}
	return out
}

func cidr(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip // the address itself, as net.Interface.Addrs gives it
	return n
}

// dual is a rig whose interface has IPv6 too: the LAN over IPv4, and the
// same interface over IPv6.
func dual(s Service) (r *rig, six *network) {
	r = newRig(s)
	addrs := ips("fe80::1", "fd00:8::1")
	r.lan.aaaa = addrs
	six = &network{ifindex: 1, name: "br-lan", v6: true, ip: addrs[0], a: lanIP.To4(), aaaa: addrs}
	r.setNetworks(r.now, []*network{r.lan, six})
	r.run(5 * time.Second)
	r.out = nil
	return r, six
}

var browser6 = &net.UDPAddr{IP: net.ParseIP("fe80::40"), Port: 5353, Zone: "br-lan"}

// Whichever family a question comes over, the answer has the host's
// addresses in both.
func TestBothFamiliesAnswered(t *testing.T) {
	r, six := dual(svc)
	r.run(time.Hour)
	for _, n := range []*network{r.lan, six} {
		from := browser
		if n.v6 {
			from = browser6
		}
		r.out = nil
		r.packet(r.now, n, from, false, query(0, "_mvd._tcp.local", typePTR))
		r.run(time.Second)
		if len(r.out) != 1 || r.out[0].to != nil {
			t.Fatalf("v6 %v: sent %+v", n.v6, r.out)
		}
		m := parseReply(t, r.out[0].msg)
		if types(m.answers) != "PTR" || types(m.extra) != "SRV TXT A AAAA AAAA NSEC" {
			t.Fatalf("v6 %v: answers %q, extra %q", n.v6, types(m.answers), types(m.extra))
		}
		a, ll, ula, nsec := m.extra[2], m.extra[3], m.extra[4], m.extra[5]
		if a.data != "192.168.8.1" || ll.data != "fe80::1" || ula.data != "fd00:8::1" ||
			ll.name != "mvd-gl-ar750s.local" || ll.ttl != 120 || ll.class != classIN|cacheFlush {
			t.Errorf("v6 %v: %+v %+v %+v", n.v6, a, ll, ula)
		}
		if nsec.data != "mvd-gl-ar750s.local A AAAA" {
			t.Errorf("v6 %v: NSEC %q", n.v6, nsec.data)
		}
	}
}

func TestAAAA(t *testing.T) {
	r, six := dual(svc)
	r.run(time.Hour)
	q := query(0, "mvd-gl-ar750s.local", typeAAAA)
	out := r.ask(browser, q)
	if len(out) != 1 {
		t.Fatalf("sent %d messages", len(out))
	}
	if m := parseReply(t, out[0].msg); types(m.answers) != "AAAA AAAA" || types(m.extra) != "A NSEC" {
		t.Errorf("answers %q, extra %q", types(m.answers), types(m.extra))
	}
	// Known answers: both addresses known, nothing's sent; one, and both
	// are (a record set goes whole).
	r.run(time.Hour)
	one := *six
	one.aaaa = six.aaaa[:1]
	if out := r.ask(browser, knowing(q, r.engine, &one, recAAAA, 120)); len(out) != 1 || types(parseReply(t, out[0].msg).answers) != "AAAA AAAA" {
		t.Errorf("one address known: %+v", out)
	}
	r.run(time.Hour)
	if out := r.ask(browser, knowing(q, r.engine, six, recAAAA, 120)); len(out) != 0 {
		t.Errorf("both addresses known: sent %d messages", len(out))
	}
}

// An interface with IPv6 only: no A record, and the NSEC says so.
func TestIPv6Only(t *testing.T) {
	r := newRig(svc)
	addrs := ips("fe80::1")
	six := &network{ifindex: 2, name: "eth1", v6: true, ip: addrs[0], aaaa: addrs}
	r.setNetworks(r.now, []*network{r.lan, six})
	r.run(5 * time.Second)
	r.out = nil
	from := &net.UDPAddr{IP: net.ParseIP("fe80::40"), Port: 5353, Zone: "eth1"}
	r.packet(r.now, six, from, false, query(0, "GL-AR750S._mvd._tcp.local", typeSRV, "mvd-gl-ar750s.local", typeA))
	if len(r.out) != 1 {
		t.Fatalf("sent %d messages", len(r.out))
	}
	m := parseReply(t, r.out[0].msg)
	if types(m.answers) != "SRV" || types(m.extra) != "AAAA NSEC" || m.extra[1].data != "mvd-gl-ar750s.local AAAA" {
		t.Errorf("answers %q, extra %q: %+v", types(m.answers), types(m.extra), m.extra)
	}
	// And the IPv4 LAN, whose interface has no IPv6, is as it was.
	out := r.ask(browser, query(0, "mvd-gl-ar750s.local", typeAAAA))
	if m := parseReply(t, out[0].msg); types(m.answers) != "" || m.extra[0].data != "mvd-gl-ar750s.local A" {
		t.Errorf("IPv4 only: %+v", m)
	}
}

// Probes and announcements carry the IPv6 addresses, and go out over
// each family.
func TestIPv6ProbedAndAnnounced(t *testing.T) {
	r := newRig(svc)
	addrs := ips("fe80::1")
	r.lan.aaaa = nil
	six := &network{ifindex: 1, name: "br-lan", v6: true, ip: addrs[0], a: lanIP.To4(), aaaa: addrs}
	var on []string
	send := r.send
	r.send = func(n *network, to *net.UDPAddr, msg []byte) {
		send(n, to, msg)
		if n.v6 {
			m := parseReply(t, msg)
			on = append(on, strings.TrimSpace(types(m.authority)+" "+types(m.answers)))
		}
	}
	r.setNetworks(r.now, []*network{r.lan, six})
	r.run(5 * time.Second)
	if got := strings.Join(on, "; "); got != "SRV TXT A AAAA; SRV TXT A AAAA; SRV TXT A AAAA; PTR PTR SRV TXT A AAAA; PTR PTR SRV TXT A AAAA" {
		t.Error(got)
	}
}

// Another machine with the same host name is a conflict over IPv6 as over
// IPv4; this machine's other addresses aren't.
func TestIPv6Conflict(t *testing.T) {
	r, six := dual(svc)
	theirs := *six
	theirs.aaaa = ips("fe80::99")
	w := newBuilder(0, 0x8400)
	w.count(1, r.write(w, recAAAA, &theirs, asUsual))
	stranger := w.b
	w = newBuilder(0, 0x8400)
	w.count(1, r.write(w, recAAAA, six, asUsual))
	mine := w.b

	r.packet(r.now, r.lan, browser, false, mine)
	r.packet(r.now, six, browser6, false, mine)
	if r.lan.phase != running || six.phase != running {
		t.Fatal("this machine's own address taken for a conflict")
	}
	r.packet(r.now, six, browser6, false, stranger)
	if r.lan.phase != probing || six.phase != probing {
		t.Error("another machine's address for the host name: not asking again")
	}
	// The same packet again, before anything's been asked, changes
	// nothing: it isn't an answer.
	r.packet(r.now, six, browser6, false, stranger)
	if r.svc.Host != "mvd-gl-ar750s" {
		t.Errorf("renamed to %s before asking", r.svc.Host)
	}
	r.run(300 * time.Millisecond) // a probe's gone out
	r.packet(r.now, six, browser6, false, stranger)
	if r.svc.Host != "mvd-gl-ar750s-2" {
		t.Errorf("host %s after an answer to the probe", r.svc.Host)
	}
}

// Which networks: private IPv4 addresses, and IPv6 on an interface with a
// link-local address, giving that and any unique local ones. Never a
// public address of either kind.
func TestList(t *testing.T) {
	ifs := []iface{
		{1, "br-lan", []*net.IPNet{cidr("192.168.8.1/24"), cidr("fd00:8::1/64"), cidr("fe80::1/64"), cidr("2001:db8::1/64")}},
		{2, "eth0", []*net.IPNet{cidr("81.2.69.160/24"), cidr("2001:db8:1::5/64"), cidr("fe80::2/64")}}, // the WAN
		{3, "br-guest", []*net.IPNet{cidr("192.168.9.1/24"), cidr("10.9.0.1/16")}},
		{4, "tun0", []*net.IPNet{cidr("fd00:99::1/64")}}, // no link-local: no mDNS over IPv6
	}
	show := func(nets []*network) string {
		var s []string
		for _, n := range nets {
			x := n.name + " " + n.ip.String()
			if n.v6 {
				x += " v6"
			}
			if n.a != nil {
				x += " A " + n.a.String()
			}
			for _, a := range n.aaaa {
				x += " AAAA " + a.String()
			}
			s = append(s, x)
		}
		return strings.Join(s, "\n")
	}
	if got, want := show(list(ifs, nil, true, true)), `br-lan 192.168.8.1 A 192.168.8.1 AAAA fe80::1 AAAA fd00:8::1
br-lan fe80::1 v6 A 192.168.8.1 AAAA fe80::1 AAAA fd00:8::1
eth0 fe80::2 v6 AAAA fe80::2
br-guest 192.168.9.1 A 192.168.9.1
br-guest 10.9.0.1 A 10.9.0.1`; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Without an IPv6 socket, the IPv4 networks still give the addresses.
	if got, want := show(list(ifs[:1], nil, true, false)), `br-lan 192.168.8.1 A 192.168.8.1 AAAA fe80::1 AAAA fd00:8::1`; got != want {
		t.Errorf("IPv4 socket only:\n%s", got)
	}
	// A server that listens on IPv4 alone gives no IPv6 address.
	only4 := func(ifname string, ip net.IP) bool { return ifname == "br-lan" && ip.To4() != nil }
	if got, want := show(list(ifs, only4, true, true)), `br-lan 192.168.8.1 A 192.168.8.1`; got != want {
		t.Errorf("IPv4 only:\n%s", got)
	}
}

func TestNetworkFor(t *testing.T) {
	nets := list([]iface{
		{1, "br-lan", []*net.IPNet{cidr("192.168.8.1/24"), cidr("fe80::1/64")}},
		{2, "br-guest", []*net.IPNet{cidr("192.168.9.1/24"), cidr("fe80::2/64")}},
	}, nil, true, true)
	if n, ok := networkFor(nets, net.IPv4(192, 168, 9, 40)); !ok || n.name != "br-guest" || n.v6 {
		t.Errorf("guest: %+v %v", n, ok)
	}
	if _, ok := networkFor(nets, net.IPv4(10, 0, 0, 2)); ok {
		t.Error("answered a query from a network not advertised on")
	}
	from := &net.UDPAddr{IP: net.ParseIP("fe80::40"), Zone: "br-guest"}
	if n, ok := networkFor6(nets, from, 0); !ok || n.name != "br-guest" || !n.v6 {
		t.Errorf("guest by zone: %+v %v", n, ok)
	}
	// The interface the system says it came in on, over the zone.
	if n, ok := networkFor6(nets, from, 1); !ok || n.name != "br-lan" || !n.v6 {
		t.Errorf("LAN by index: %+v %v", n, ok)
	}
	if _, ok := networkFor6(nets, &net.UDPAddr{IP: net.ParseIP("2001:db8::7")}, 1); ok {
		t.Error("answered a query from a public address")
	}
	if _, ok := networkFor6(nets, from, 7); ok {
		t.Error("answered a query from an interface not advertised on")
	}
}

func TestAdvertisable(t *testing.T) {
	for ip, want := range map[string]bool{"192.168.8.1": true, "10.0.0.2": true, "172.16.0.9": true,
		"169.254.3.4": true, "81.2.69.160": false, "127.0.0.1": false,
		"fe80::1": true, "fd00:8::1": true, "2001:db8::1": false, "::1": false} {
		if got := advertisable(net.ParseIP(ip)); got != want {
			t.Errorf("%s: %v", ip, got)
		}
	}
}
