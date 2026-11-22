package mdns

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A wire is a pretend network: the servers on it hear each other, and
// themselves, as on a real one.
type wire struct {
	now   time.Time
	hosts []*host
	queue []delivery
}

type host struct {
	*engine
	n    *network
	logs []string
	out  []sent // what it's sent
}

type delivery struct {
	from *host
	to   *net.UDPAddr
	msg  []byte
}

func newWire() *wire { return &wire{now: time.Unix(1_700_000_000, 0)} }

// add starts a server on the wire, at the address ip.
func (w *wire) add(s Service, ip string) *host {
	h := &host{}
	h.engine = newEngine(s, func(n *network, to *net.UDPAddr, msg []byte) {
		msg = append([]byte(nil), msg...)
		h.out = append(h.out, sent{to, msg})
		w.queue = append(w.queue, delivery{h, to, msg})
	}, func(f string, a ...any) { h.logs = append(h.logs, fmt.Sprintf(f, a...)) })
	h.rand = func(d time.Duration) time.Duration { return d / 2 }
	_, lan, _ := net.ParseCIDR("192.168.8.0/24")
	h.n = &network{ifindex: 1, name: "br-lan", ip: net.ParseIP(ip).To4(), net: lan, a: net.ParseIP(ip).To4()}
	h.setNetworks(w.now, []*network{h.n})
	w.hosts = append(w.hosts, h)
	return h
}

// run moves the clock on by d, delivering what's sent.
func (w *wire) run(d time.Duration) {
	end := w.now.Add(d)
	for {
		for len(w.queue) > 0 {
			m := w.queue[0]
			w.queue = w.queue[1:]
			for _, h := range w.hosts {
				if m.to == nil || m.to.IP.Equal(h.n.ip) {
					h.packet(w.now, h.n, &net.UDPAddr{IP: m.from.n.ip, Port: 5353}, m.to != nil, m.msg)
				}
			}
		}
		var next time.Time
		for _, h := range w.hosts {
			if t := h.next(); !t.IsZero() && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
		if next.IsZero() || next.After(end) {
			break
		}
		if next.After(w.now) {
			w.now = next
		}
		for _, h := range w.hosts {
			if t := h.next(); !t.IsZero() && !t.After(w.now) {
				h.tick(w.now)
			}
		}
	}
	w.now = end
}

func (h *host) names() string { return h.svc.Instance + " " + h.svc.Host }

// RFC 6762 section 8.1: before using its names the service asks whether
// anyone has them, three times, 250 ms apart, then announces twice.
func TestProbeThenAnnounce(t *testing.T) {
	w := newWire()
	h := w.add(svc, "192.168.8.1")
	var at []int
	start := w.now
	for i := 0; i < 3000; i++ {
		n := len(h.out)
		w.run(time.Millisecond)
		for range h.out[n:] {
			at = append(at, int(w.now.Sub(start).Milliseconds()))
		}
	}
	// The first after a random wait of up to 250 ms: the test's is half.
	if ints(at) != "125 375 625 875 1875" {
		t.Fatalf("sent at %v ms", at)
	}
	for i, m := range h.out {
		r := parseReply(t, m.msg)
		if m.to != nil {
			t.Errorf("message %d: sent to %v", i, m.to)
		}
		if i >= 3 {
			if r.flags != 0x8400 || types(r.answers) != "PTR PTR SRV TXT A" {
				t.Errorf("announcement %d: flags %#x, %q", i-3, r.flags, types(r.answers))
			}
			continue
		}
		if r.flags != 0 || r.questions != 2 || len(r.answers) != 0 || types(r.authority) != "SRV TXT A" {
			t.Fatalf("probe %d: flags %#x, %d questions, answers %q, authority %q", i, r.flags, r.questions, types(r.answers), types(r.authority))
		}
		for _, x := range r.authority {
			// Not the service's yet: nothing tells caches to drop what they hold.
			if x.class != classIN || x.ttl == 0 {
				t.Errorf("probe %d: %+v", i, x)
			}
		}
	}
	// The questions: any record (type 255) of the instance and of the
	// host, answers wanted by multicast (the top bit of the class clear).
	if p, q := string(h.out[0].msg[12:]), "\x09GL-AR750S\x04_mvd\x04_tcp\x05local\x00\x00\xff\x00\x01\x0dmvd-gl-ar750s\xc0\x20\x00\xff\x00\x01"; !strings.HasPrefix(p, q) {
		t.Errorf("probe's questions: %q", p[:len(q)])
	}
	if len(h.logs) != 1 || h.logs[0] != `mvd: Bonjour: "GL-AR750S" on br-lan (192.168.8.1)` {
		t.Errorf("log: %q", h.logs)
	}
}

// Until it's asked and heard nothing, the names aren't the service's to
// answer for.
func TestNothingAnsweredWhileProbing(t *testing.T) {
	w := newWire()
	h := w.add(svc, "192.168.8.1")
	w.run(500 * time.Millisecond)
	n := len(h.out)
	for _, q := range [][]byte{query(0, "_mvd._tcp.local", typePTR), query(0, "GL-AR750S._mvd._tcp.local", typeSRV), query(7, "mvd-gl-ar750s.local", typeA)} {
		h.packet(w.now, h.n, browser, false, q)
		h.packet(w.now, h.n, browser, true, q)
		h.packet(w.now, h.n, resolver, false, q)
	}
	if len(h.out) != n {
		t.Errorf("answered: %d messages", len(h.out)-n)
	}
}

// A second server with the same names, on another machine, takes the next
// ones: both are listed.
func TestSecondServerTakesAnotherName(t *testing.T) {
	w := newWire()
	a := w.add(svc, "192.168.8.1")
	w.run(10 * time.Second)
	b := w.add(svc, "192.168.8.2")
	w.run(10 * time.Second)
	if a.names() != "GL-AR750S mvd-gl-ar750s" || b.names() != "GL-AR750S (2) mvd-gl-ar750s-2" {
		t.Fatalf("a: %s; b: %s", a.names(), b.names())
	}
	if len(a.logs) != 1 {
		t.Errorf("a's log: %q", a.logs)
	}
	if got := strings.Join(b.logs, "\n"); got != `mvd: Bonjour: another machine is mvd-gl-ar750s.local: this one is now mvd-gl-ar750s-2.local
mvd: Bonjour: there's another "GL-AR750S" on the network: this one is now "GL-AR750S (2)"
mvd: Bonjour: "GL-AR750S (2)" on br-lan (192.168.8.2)` {
		t.Errorf("b's log:\n%s", got)
	}
	// A browser now hears of both.
	a.out, b.out = nil, nil
	for _, h := range w.hosts {
		h.packet(w.now, h.n, browser, false, query(0, "_mvd._tcp.local", typePTR))
	}
	w.run(time.Second)
	var found []string
	for _, h := range w.hosts {
		if len(h.out) != 1 {
			t.Fatalf("%s sent %d messages", h.names(), len(h.out))
		}
		r := parseReply(t, h.out[0].msg)
		found = append(found, r.answers[0].data+" at "+r.extra[2].name+" "+r.extra[2].data)
	}
	if got := strings.Join(found, "; "); got != "GL-AR750S._mvd._tcp.local at mvd-gl-ar750s.local 192.168.8.1; GL-AR750S (2)._mvd._tcp.local at mvd-gl-ar750s-2.local 192.168.8.2" {
		t.Error(got)
	}
	// And neither has anything more to say.
	w.run(time.Minute)
	if len(a.out)+len(b.out) != 2 || a.names()+b.names() != "GL-AR750S mvd-gl-ar750sGL-AR750S (2) mvd-gl-ar750s-2" {
		t.Errorf("after a minute: %d more messages; %s; %s", len(a.out)+len(b.out)-2, a.names(), b.names())
	}
}

// Section 8.2: two servers switched on together ask at the same moment.
// One gives way, and it's settled without either answering.
func TestTwoStartTogether(t *testing.T) {
	w := newWire()
	a := w.add(svc, "192.168.8.1")
	b := w.add(svc, "192.168.8.2")
	w.run(20 * time.Second)
	// The higher address is the higher record: it keeps the names.
	if a.names() != "GL-AR750S (2) mvd-gl-ar750s-2" || b.names() != "GL-AR750S mvd-gl-ar750s" {
		t.Errorf("a: %s; b: %s", a.names(), b.names())
	}
	if a.n.phase != running || b.n.phase != running {
		t.Errorf("phases %d, %d", a.n.phase, b.n.phase)
	}
}

// Two servers on one machine (the real one and a test one, on another
// port) share the machine's host name, which is no conflict: it's the
// same address. The instance is one, as the ports differ.
func TestTwoOnOneMachine(t *testing.T) {
	w := newWire()
	a := w.add(svc, "192.168.8.1")
	w.run(10 * time.Second)
	other := svc
	other.Port = 6838
	b := w.add(other, "192.168.8.1")
	w.run(10 * time.Second)
	if a.names() != "GL-AR750S mvd-gl-ar750s" || b.names() != "GL-AR750S (2) mvd-gl-ar750s" {
		t.Errorf("a: %s; b: %s", a.names(), b.names())
	}
}

// Section 9: two servers each sure of the same name (two networks joined,
// say) find out when one hears the other. Both ask again; one keeps it.
func TestConflictLater(t *testing.T) {
	w1, w2 := newWire(), newWire()
	a := w1.add(svc, "192.168.8.1")
	b := w2.add(svc, "192.168.8.2")
	w1.run(10 * time.Second)
	w2.run(10 * time.Second)
	w1.hosts = append(w1.hosts, b) // now they can hear each other
	b.send = a.send
	send := a.send
	a.send = func(n *network, to *net.UDPAddr, msg []byte) { send(n, to, msg); w1.queue[len(w1.queue)-1].from = a }
	b.send = func(n *network, to *net.UDPAddr, msg []byte) { send(n, to, msg); w1.queue[len(w1.queue)-1].from = b }
	if a.names() != b.names() {
		t.Fatalf("a: %s; b: %s", a.names(), b.names())
	}
	for _, h := range w1.hosts {
		h.packet(w1.now, h.n, browser, false, query(0, "_mvd._tcp.local", typePTR))
	}
	w1.run(20 * time.Second)
	if a.names() != "GL-AR750S (2) mvd-gl-ar750s-2" || b.names() != "GL-AR750S mvd-gl-ar750s" {
		t.Errorf("a: %s; b: %s", a.names(), b.names())
	}
}

// The same data is no conflict: it's the service's own message come back
// (the system loops multicast back), or its address on another of this
// machine's interfaces.
func TestOwnRecordsAreNoConflict(t *testing.T) {
	r := newRig(svc)
	_, wifi, _ := net.ParseCIDR("192.168.8.0/24")
	r.setNetworks(r.now, []*network{r.lan, {ifindex: 2, name: "wlan0", ip: net.IPv4(192, 168, 8, 9).To4(), net: wifi, a: net.IPv4(192, 168, 8, 9).To4()}})
	r.run(5 * time.Second)
	for _, n := range r.nets {
		for _, other := range r.nets {
			r.announce(r.now, other, false)
			r.sendProbe(other)
			msgs := r.out[len(r.out)-2:]
			for _, m := range msgs {
				r.packet(r.now, n, &net.UDPAddr{IP: other.ip, Port: 5353}, false, m.msg)
			}
		}
	}
	r.out = nil
	r.run(5 * time.Second)
	if r.svc.Instance != "GL-AR750S" || r.svc.Host != "mvd-gl-ar750s" || len(r.out) != 0 {
		t.Errorf("%s %s, %d messages sent", r.svc.Instance, r.svc.Host, len(r.out))
	}
	for _, n := range r.nets {
		if n.phase != running {
			t.Errorf("%s: phase %d", n.name, n.phase)
		}
	}
}

// A goodbye for the name isn't someone using it.
func TestGoodbyeIsNoConflict(t *testing.T) {
	other := svc
	other.Port = 80
	o := newRig(other)
	o.announce(o.now, o.lan, true)
	r := newRig(svc)
	r.packet(r.now, r.lan, browser, false, o.out[0].msg)
	if r.lan.phase != running {
		t.Error("asking again, after a goodbye")
	}
}

// A probe is answered at once, even for a record that's just gone out:
// whoever's asking has 250 ms to hear the name's taken.
func TestProbeAnsweredAtOnce(t *testing.T) {
	other := svc
	other.Port = 80
	o := newRig(other)
	o.sendProbe(o.lan)
	probe := o.out[0].msg

	r := newRig(svc)
	r.ask(browser, query(0, "GL-AR750S._mvd._tcp.local", typeSRV)) // the SRV's just been multicast
	r.out = nil
	r.packet(r.now, r.lan, browser, false, probe)
	if len(r.out) != 1 || r.out[0].to != nil {
		t.Fatalf("sent %+v", r.out)
	}
	if m := parseReply(t, r.out[0].msg); types(m.answers) != "SRV TXT" {
		t.Errorf("answers %q, extra %q", types(m.answers), types(m.extra))
	}
}

// The tiebreak: of two asking at once, the one whose records sort lower
// waits a second.
func TestTiebreak(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(s *Service)
		lose   bool
	}{
		{"the same", func(s *Service) {}, false},
		{"a higher port", func(s *Service) { s.Port++ }, true},
		{"a lower port", func(s *Service) { s.Port-- }, false},
		{"a later host", func(s *Service) { s.Host = "mvd-zz-ar750s" }, true}, // as long, so the letters decide
		{"an earlier host", func(s *Service) { s.Host = "mvd-a" }, false},
		{"later TXT", func(s *Service) { s.TXT = []string{"version=v1.3", "auth=token"} }, true}, // TXT sorts before SRV: it decides
		{"earlier TXT, a higher port", func(s *Service) { s.TXT = []string{"version=v1.1"}; s.Port++ }, false},
	} {
		other := svc
		c.change(&other)
		o := newRig(other)
		o.sendProbe(o.lan)
		w := newWire()
		h := w.add(svc, "192.168.8.1")
		w.run(200 * time.Millisecond) // one probe sent, the next due at 375 ms
		h.packet(w.now, h.n, browser, false, o.out[0].msg)
		if got := h.n.at.Sub(w.now) == time.Second; got != c.lose {
			t.Errorf("%s: next probe in %v", c.name, h.n.at.Sub(w.now))
		}
	}
}

func TestNumberedNamesFit(t *testing.T) {
	s := svc
	s.Instance = strings.Repeat("é", 31) + "x" // 63 bytes
	s.Host = strings.Repeat("h", 63)
	e := newEngine(s, nil, nil)
	e.instanceN, e.hostN = 12, 12
	e.name()
	if e.svc.Instance != strings.Repeat("é", 29)+" (12)" || e.svc.Host != strings.Repeat("h", 60)+"-12" {
		t.Errorf("%q (%d bytes), %q", e.svc.Instance, len(e.svc.Instance), e.svc.Host)
	}
}

// Section 8.1: after 15 conflicts in quick succession, each new try waits
// five seconds, so a network where the name can't be had isn't flooded.
func TestManyConflictsSlowDown(t *testing.T) {
	other := svc
	other.Port = 80
	w := newWire()
	h := w.add(svc, "192.168.8.1")
	var waits []time.Duration
	for i := 0; i < 16; i++ {
		w.run(h.n.at.Sub(w.now) + time.Millisecond) // it asks
		// Whatever name it tries, someone has it.
		other.Instance = h.svc.Instance
		o := newRig(other)
		o.announce(o.now, o.lan, false)
		h.packet(w.now, h.n, browser, false, o.out[len(o.out)-1].msg)
		waits = append(waits, h.n.at.Sub(w.now))
	}
	if waits[13] != 125*time.Millisecond || waits[14] != 5*time.Second || waits[15] != 5*time.Second || h.svc.Instance != "GL-AR750S (17)" {
		t.Errorf("waits %v; now %q", waits, h.svc.Instance)
	}
}

// An NSEC for the instance says what it has (SRV and TXT), so a question
// for anything else is answered "no" rather than left hanging.
func TestInstanceNSEC(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(0, "GL-AR750S._mvd._tcp.local", typeA), false))
	if len(r.answers) != 0 || types(r.extra) != "NSEC" {
		t.Fatalf("answers %q, extra %q", types(r.answers), types(r.extra))
	}
	if n := r.extra[0]; n.name != "GL-AR750S._mvd._tcp.local" || n.data != "GL-AR750S._mvd._tcp.local TXT SRV" || n.ttl != 120 {
		t.Errorf("%+v", n)
	}
}
