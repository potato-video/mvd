package mdns

import (
	"encoding/binary"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

var svc = Service{Instance: "GL-AR750S", Type: "_mvd._tcp", Host: "mvd-gl-ar750s", Port: 6837,
	TXT: []string{"version=v1.2", "auth=token"}}

var lanIP = net.IPv4(192, 168, 8, 1)

// A message the engine sent: to the group (to nil), or to one address.
type sent struct {
	to  *net.UDPAddr
	msg []byte
}

// rig is an engine with one network (the LAN, 192.168.8.1/24), already
// announced on, and a clock the test moves. Messages it sends collect in
// out.
type rig struct {
	*engine
	lan *network
	now time.Time
	out []sent
}

func newRig(s Service) *rig {
	r := &rig{now: time.Unix(1_700_000_000, 0)}
	r.engine = newEngine(s, func(n *network, to *net.UDPAddr, msg []byte) {
		r.out = append(r.out, sent{to, append([]byte(nil), msg...)})
	}, func(string, ...any) {})
	r.rand = func(d time.Duration) time.Duration { return d / 2 }
	_, lan, _ := net.ParseCIDR("192.168.8.1/24")
	r.lan = &network{ifindex: 1, name: "br-lan", ip: lanIP.To4(), net: lan, a: lanIP.To4()}
	r.setNetworks(r.now, []*network{r.lan})
	r.run(5 * time.Second) // the announcements
	r.out = nil
	return r
}

// run moves the clock on by d, ticking whenever the engine asks.
func (r *rig) run(d time.Duration) {
	end := r.now.Add(d)
	for {
		next := r.next()
		if next.IsZero() || next.After(end) {
			break
		}
		if next.After(r.now) {
			r.now = next
		}
		r.tick(r.now)
	}
	r.now = end
}

var (
	browser  = &net.UDPAddr{IP: net.IPv4(192, 168, 8, 40), Port: 5353}
	resolver = &net.UDPAddr{IP: net.IPv4(192, 168, 8, 41), Port: 49152} // a one-shot one, not on 5353
)

// ask sends the engine a query, addressed to the group, and returns what
// it sent in the next second.
func (r *rig) ask(from *net.UDPAddr, q []byte) []sent {
	r.out = nil
	r.packet(r.now, r.lan, from, false, q)
	r.run(time.Second)
	return r.out
}

// answer is the one message a fresh engine multicasts in reply to a query
// from another mDNS responder (legacy: sends straight back to a one-shot
// resolver); nil if it sends nothing.
func answer(t *testing.T, s Service, q []byte, legacy bool) []byte {
	t.Helper()
	r := newRig(s)
	r.run(time.Hour) // nothing's been multicast lately
	from := browser
	if legacy {
		from = resolver
	}
	out := r.ask(from, q)
	switch {
	case len(out) == 0:
		return nil
	case len(out) > 1:
		t.Fatalf("%d messages sent", len(out))
	case legacy != (out[0].to != nil):
		t.Fatalf("sent to %v", out[0].to)
	}
	return out[0].msg
}

// rr is a record read back from a response, with its data decoded enough
// to check.
type rr struct {
	name  string
	rtype uint16
	class uint16
	ttl   uint32
	data  string // PTR/SRV target, A address, TXT strings joined by "|", NSEC "next types"
	port  uint16
}

type reply struct {
	id, flags uint16
	questions int
	answers   []rr
	authority []rr
	extra     []rr
}

func dotted(n name) string { return strings.Join(n, ".") }

// parseReply reads a whole response, the way a browser would.
func parseReply(t *testing.T, b []byte) reply {
	t.Helper()
	if len(b) < 12 {
		t.Fatalf("short message: %d bytes", len(b))
	}
	h := func(i int) uint16 { return binary.BigEndian.Uint16(b[i:]) }
	r := reply{id: h(0), flags: h(2), questions: int(h(4))}
	off := 12
	for i := 0; i < r.questions; i++ {
		_, end, err := readName(b, off)
		if err != nil {
			t.Fatal(err)
		}
		off = end + 4
	}
	read := func(n int) []rr {
		var out []rr
		for i := 0; i < n; i++ {
			nm, end, err := readName(b, off)
			if err != nil {
				t.Fatal(err)
			}
			x := rr{name: dotted(nm), rtype: h(end), class: h(end + 2), ttl: binary.BigEndian.Uint32(b[end+4:])}
			dl := int(h(end + 8))
			d := end + 10
			switch x.rtype {
			case typePTR:
				n, _, err := readName(b, d)
				if err != nil {
					t.Fatal(err)
				}
				x.data = dotted(n)
			case typeSRV:
				x.port = h(d + 4)
				n, _, err := readName(b, d+6)
				if err != nil {
					t.Fatal(err)
				}
				x.data = dotted(n)
			case typeA, typeAAAA:
				x.data = net.IP(b[d : d+dl]).String()
			case typeTXT:
				var parts []string
				for p := d; p < d+dl; p += 1 + int(b[p]) {
					parts = append(parts, string(b[p+1:p+1+int(b[p])]))
				}
				x.data = strings.Join(parts, "|")
			case typeNSEC:
				n, e, err := readName(b, d)
				if err != nil {
					t.Fatal(err)
				}
				x.data = dotted(n)
				// One window (0) of types: say which are set.
				if b[e] == 0 {
					for i, by := range b[e+2 : d+dl] {
						for bit := 0; bit < 8; bit++ {
							if by&(0x80>>bit) != 0 {
								x.data += " " + map[int]string{typeA: "A", typeTXT: "TXT", typeSRV: "SRV", typeAAAA: "AAAA"}[i*8+bit]
							}
						}
					}
				}
			}
			out = append(out, x)
			off = d + dl
		}
		return out
	}
	r.answers = read(int(h(6)))
	r.authority = read(int(h(8)))
	r.extra = read(int(h(10)))
	if off != len(b) {
		t.Errorf("%d bytes left over", len(b)-off)
	}
	return r
}

// query builds a question message, as a browser sends one.
func query(id uint16, qs ...any) []byte {
	b := binary.BigEndian.AppendUint16(nil, id)
	b = append(b, 0, 0)
	b = binary.BigEndian.AppendUint16(b, uint16(len(qs)/2))
	b = append(b, 0, 0, 0, 0, 0, 0)
	for i := 0; i < len(qs); i += 2 {
		for _, l := range strings.Split(qs[i].(string), ".") {
			b = append(b, byte(len(l)))
			b = append(b, l...)
		}
		b = append(b, 0)
		b = binary.BigEndian.AppendUint16(b, uint16(qs[i+1].(int)))
		b = binary.BigEndian.AppendUint16(b, classIN|uint16(qs[i+1].(int)>>16<<15))
	}
	return b
}

// QU, added to a question's type for query: it asks for a unicast reply.
const QU = 1 << 16

func types(rs []rr) string {
	var s []string
	for _, r := range rs {
		s = append(s, map[uint16]string{typePTR: "PTR", typeSRV: "SRV", typeTXT: "TXT", typeA: "A", typeAAAA: "AAAA", typeNSEC: "NSEC"}[r.rtype])
	}
	return strings.Join(s, " ")
}

func TestBrowse(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(0, "_mvd._tcp.local", typePTR), false))
	if r.flags != 0x8400 || r.questions != 0 || r.id != 0 {
		t.Errorf("header: flags %#x, %d questions, id %d", r.flags, r.questions, r.id)
	}
	if types(r.answers) != "PTR" || types(r.extra) != "SRV TXT A NSEC" {
		t.Fatalf("answers %q, extra %q", types(r.answers), types(r.extra))
	}
	ptr := r.answers[0]
	if ptr.name != "_mvd._tcp.local" || ptr.data != "GL-AR750S._mvd._tcp.local" || ptr.class != classIN || ptr.ttl != 4500 {
		t.Errorf("PTR: %+v", ptr)
	}
	srv, txt, a, nsec := r.extra[0], r.extra[1], r.extra[2], r.extra[3]
	if srv.name != "GL-AR750S._mvd._tcp.local" || srv.data != "mvd-gl-ar750s.local" || srv.port != 6837 ||
		srv.class != classIN|cacheFlush || srv.ttl != 120 {
		t.Errorf("SRV: %+v", srv)
	}
	if txt.data != "version=v1.2|auth=token" || txt.ttl != 4500 || txt.class != classIN|cacheFlush {
		t.Errorf("TXT: %+v", txt)
	}
	if a.name != "mvd-gl-ar750s.local" || a.data != "192.168.8.1" || a.ttl != 120 || a.class != classIN|cacheFlush {
		t.Errorf("A: %+v", a)
	}
	if nsec.name != "mvd-gl-ar750s.local" || nsec.data != "mvd-gl-ar750s.local A" {
		t.Errorf("NSEC: %+v", nsec)
	}
}

func TestNamesIgnoreCase(t *testing.T) {
	if answer(t, svc, query(0, "_MVD._TCP.Local", typePTR), false) == nil {
		t.Error("no answer to an upper-case question")
	}
	if answer(t, svc, query(0, "gl-ar750s._mvd._tcp.local", typeSRV), false) == nil {
		t.Error("no answer for the instance in lower case")
	}
}

func TestOtherQuestionsUnanswered(t *testing.T) {
	for _, q := range []struct {
		name  string
		qtype int
	}{
		{"_airplay._tcp.local", typePTR},
		{"_mvd._tcp.local", typeA},
		{"other._mvd._tcp.local", typeSRV},
		{"gl-ar750s.local", typeA}, // the router's own name is the system responder's
	} {
		if b := answer(t, svc, query(0, q.name, q.qtype), false); b != nil {
			t.Errorf("%s %d: answered", q.name, q.qtype)
		}
	}
}

func TestResponsesIgnored(t *testing.T) {
	q := query(0, "_mvd._tcp.local", typePTR)
	q[2] = 0x84 // QR: someone else's answer
	if answer(t, svc, q, false) != nil {
		t.Error("answered a response")
	}
}

func TestBadMessages(t *testing.T) {
	q := query(0, "_mvd._tcp.local", typePTR)
	for i := 0; i < len(q); i++ {
		answer(t, svc, q[:i], false) // mustn't panic
	}
	loop := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 12, 0, 12, 0, 1}
	if answer(t, svc, loop, false) != nil {
		t.Error("answered a name that points at itself")
	}
}

func TestResolve(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(0, "GL-AR750S._mvd._tcp.local", typeSRV, "GL-AR750S._mvd._tcp.local", typeTXT), false))
	if types(r.answers) != "SRV TXT" || types(r.extra) != "A NSEC" {
		t.Errorf("answers %q, extra %q", types(r.answers), types(r.extra))
	}
	r = parseReply(t, answer(t, svc, query(0, "mvd-gl-ar750s.local", typeA), false))
	if types(r.answers) != "A" || types(r.extra) != "NSEC" {
		t.Errorf("A: answers %q, extra %q", types(r.answers), types(r.extra))
	}
	// No IPv6 address: say so, rather than leave the asker waiting.
	r = parseReply(t, answer(t, svc, query(0, "mvd-gl-ar750s.local", typeAAAA), false))
	if types(r.answers) != "" || types(r.extra) != "NSEC" {
		t.Errorf("AAAA: answers %q, extra %q", types(r.answers), types(r.extra))
	}
}

func TestServiceTypes(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(0, "_services._dns-sd._udp.local", typePTR), false))
	if types(r.answers) != "PTR" || r.answers[0].data != "_mvd._tcp.local" {
		t.Errorf("%+v", r.answers)
	}
}

func TestAny(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(0, "GL-AR750S._mvd._tcp.local", typeANY), false))
	if types(r.answers) != "SRV TXT" {
		t.Errorf("answers %q", types(r.answers))
	}
}

// A query from a port other than 5353 is a one-shot (legacy) resolver's: the
// reply goes straight back to it, with its id and questions, short TTLs and
// no cache-flush bit.
func TestLegacyQuery(t *testing.T) {
	r := parseReply(t, answer(t, svc, query(1234, "_mvd._tcp.local", typePTR), true))
	if r.id != 1234 || r.questions != 1 {
		t.Errorf("id %d, %d questions", r.id, r.questions)
	}
	for _, x := range append(r.answers, r.extra...) {
		if x.ttl > 10 || x.class != classIN {
			t.Errorf("%s: ttl %d class %#x", x.name, x.ttl, x.class)
		}
	}
}

// announcement is what the engine multicasts to say the service is here,
// or (goodbye) going.
func announcement(s Service, goodbye bool) []byte {
	r := newRig(s)
	r.announce(r.now, r.lan, goodbye)
	return r.out[0].msg
}

func TestAnnounce(t *testing.T) {
	r := parseReply(t, announcement(svc, false))
	if r.flags != 0x8400 || types(r.answers) != "PTR PTR SRV TXT A" || len(r.extra) != 0 {
		t.Errorf("flags %#x, answers %q, extra %q", r.flags, types(r.answers), types(r.extra))
	}
	if r.answers[1].name != "_services._dns-sd._udp.local" {
		t.Errorf("second PTR: %+v", r.answers[1])
	}
	for _, x := range parseReply(t, announcement(svc, true)).answers {
		if x.ttl != 0 {
			t.Errorf("goodbye: %s ttl %d", x.name, x.ttl)
		}
	}
}

// Names are compressed: the instance's name follows the type's.
func TestCompression(t *testing.T) {
	b := announcement(svc, false)
	if n := strings.Count(string(b), "_mvd"); n != 1 {
		t.Errorf("_mvd written %d times", n)
	}
}

func TestLongInstanceName(t *testing.T) {
	s := svc
	s.Instance = strings.Repeat("é", 40) // 80 bytes: more than a label holds
	r := parseReply(t, answer(t, s, query(0, "_mvd._tcp.local", typePTR), false))
	inst := strings.TrimSuffix(r.answers[0].data, "._mvd._tcp.local")
	if len(inst) > 63 || inst != strings.Repeat("é", 31) {
		t.Errorf("%d bytes: %q", len(inst), inst)
	}
}

func TestEmptyTXT(t *testing.T) {
	s := svc
	s.TXT = nil
	r := parseReply(t, announcement(s, false))
	// A TXT record can't be empty: it holds one empty string.
	if r.answers[3].rtype != typeTXT || r.answers[3].data != "" {
		t.Errorf("%+v", r.answers[3])
	}
}

// Review L11: a packet can't make much work. One 9,000-byte packet of
// 65,535 questions, each pointing at one long name, built megabytes of
// strings, and did so before checking whether it was a query at all.
func TestQueryWorkIsCapped(t *testing.T) {
	// A long name (many one-byte labels) at offset 12, then questions that
	// each point back at it.
	msg := []byte{0, 0, 0, 0, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0}
	for i := 0; i < 120; i++ {
		msg = append(msg, 1, 'a')
	}
	msg = append(msg, 0, 0, 12, 0, 1)
	for len(msg) < 9000-6 {
		msg = append(msg, 0xC0, 12, 0, 12, 0, 1)
	}
	r := newRig(svc)
	measure := func(q []byte) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r.packet(r.now, r.lan, browser, false, q)
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	if n := measure(msg); n > 1<<10 {
		t.Errorf("a query packet cost %d bytes", n)
	}
	resp := append([]byte(nil), msg...)
	resp[2] = 0x84 // a response: not to be parsed at all
	if n := measure(resp); n > 1<<10 {
		t.Errorf("a response packet cost %d bytes", n)
	}
}
