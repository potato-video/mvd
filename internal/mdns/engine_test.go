package mdns

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// knowing adds known answers to a query: these records as e would send
// them, with this TTL left.
func knowing(q []byte, e *engine, n *network, which records, ttl uint32) []byte {
	// Written into a message of their own, then moved over: so no names
	// compressed, as the offsets would be wrong.
	w := newBuilder(0, 0)
	w.names = nil
	c := e.write(w, which, n, asUsual)
	recs := w.b[12:]
	for off := 0; off < len(recs); {
		r, end, _ := readRecord(recs, off)
		binary.BigEndian.PutUint32(recs[r.data-6:], ttl)
		binary.BigEndian.PutUint16(recs[r.data-8:], r.class&^cacheFlush) // never set in a known answer
		off = end
	}
	q = append(append([]byte(nil), q...), recs...)
	binary.BigEndian.PutUint16(q[6:], binary.BigEndian.Uint16(q[6:])+uint16(c))
	return q
}

// RFC 6762 section 7.1: an answer the asker lists as known, with at least
// half its TTL left, isn't sent. A device browsing for everything asks for
// _mvd._tcp again and again, listing the servers it knows each time.
func TestKnownAnswersNotSent(t *testing.T) {
	r := newRig(svc)
	browse := query(0, "_mvd._tcp.local", typePTR)
	if out := r.ask(browser, knowing(browse, r.engine, r.lan, recPTR, 4500)); len(out) != 0 {
		t.Errorf("known: sent %d messages", len(out))
	}
	if out := r.ask(browser, knowing(browse, r.engine, r.lan, recPTR, 2250)); len(out) != 0 {
		t.Errorf("known, half its TTL left: sent %d messages", len(out))
	}
	if out := r.ask(browser, knowing(browse, r.engine, r.lan, recPTR, 2249)); len(out) != 1 {
		t.Errorf("known, under half its TTL left: sent %d messages", len(out))
	}
	// Another server's PTR is known: this one's is still wanted.
	other := svc
	other.Instance = "MBP14"
	if out := r.ask(browser, knowing(browse, newRig(other).engine, r.lan, recPTR, 4500)); len(out) != 1 {
		t.Errorf("another server known: sent %d messages", len(out))
	}
}

// A known answer has to be the same record: the same data, not only the
// same name.
func TestKnownAnswersMustMatch(t *testing.T) {
	q := query(0, "GL-AR750S._mvd._tcp.local", typeSRV, "GL-AR750S._mvd._tcp.local", typeTXT, "mvd-gl-ar750s.local", typeA)
	lan2 := *newRig(svc).lan
	lan2.ip = net.IPv4(192, 168, 8, 2).To4()
	lan2.a = lan2.ip
	for _, c := range []struct {
		name   string
		change func(s *Service)
		n      *network
		want   string // the answers still sent
	}{
		{"the same", func(s *Service) {}, nil, ""},
		{"another port", func(s *Service) { s.Port = 80 }, nil, "SRV"},
		{"another host", func(s *Service) { s.Host = "mvd-other" }, nil, "SRV A"},
		{"other TXT", func(s *Service) { s.TXT = []string{"version=v1.1", "auth=token"} }, nil, "TXT"},
		{"less TXT", func(s *Service) { s.TXT = s.TXT[:1] }, nil, "TXT"},
		{"more TXT", func(s *Service) { s.TXT = append(s.TXT[:2:2], "x=y") }, nil, "TXT"},
		{"no TXT", func(s *Service) { s.TXT = nil }, nil, "TXT"},
		{"another address", func(s *Service) {}, &lan2, "A"},
	} {
		r := newRig(svc)
		s := svc
		c.change(&s)
		n := r.lan
		if c.n != nil {
			n = c.n
		}
		out := r.ask(browser, knowing(q, newRig(s).engine, n, recSRV|recTXT|recA, 4500))
		got := ""
		if len(out) == 1 {
			got = types(parseReply(t, out[0].msg).answers)
		}
		if got != c.want {
			t.Errorf("%s: sent %q, want %q", c.name, got, c.want)
		}
	}
	// Upper case is the same name.
	r := newRig(svc)
	s := svc
	s.Host = strings.ToUpper(s.Host)
	if out := r.ask(browser, knowing(q, newRig(s).engine, r.lan, recSRV|recTXT|recA, 4500)); len(out) != 0 {
		t.Errorf("upper-case host: sent %d messages", len(out))
	}
}

// What goes along unasked follows the answers that are sent: knowing the
// SRV but not the PTR gets the PTR and the SRV anyway, as an additional.
func TestKnownAnswersAndAdditionals(t *testing.T) {
	r := newRig(svc)
	out := r.ask(browser, knowing(query(0, "_mvd._tcp.local", typePTR, "GL-AR750S._mvd._tcp.local", typeSRV), r.engine, r.lan, recSRV, 120))
	if len(out) != 1 {
		t.Fatalf("sent %d messages", len(out))
	}
	if m := parseReply(t, out[0].msg); types(m.answers) != "PTR" || types(m.extra) != "SRV TXT A NSEC" {
		t.Errorf("answers %q, extra %q", types(m.answers), types(m.extra))
	}
}

// Everyone's mDNS traffic arrives here: a packet that isn't for the
// service mustn't leave garbage for the collector on a router.
func TestOthersPacketsCostNothing(t *testing.T) {
	r := newRig(svc)
	q := query(0, "_airplay._tcp.local", typePTR, "Living Room._airplay._tcp.local", typeSRV)
	resp := append([]byte(nil), q...)
	resp[2] = 0x84
	for name, msg := range map[string][]byte{"query": q, "response": resp} {
		if n := testing.AllocsPerRun(100, func() { r.packet(r.now, r.lan, browser, false, msg) }); n != 0 {
			t.Errorf("%s: %v allocations", name, n)
		}
	}
}

// RFC 6762 section 5.5: a query sent to this machine alone, not to the
// group, is answered to its sender alone. A Mac refreshing a resolved
// service does this every minute or two, and mustn't have the answer sent
// to the whole network each time.
func TestDirectQueryAnsweredDirectly(t *testing.T) {
	r := newRig(svc)
	for i := 0; i < 3; i++ {
		r.run(90 * time.Second)
		r.out = nil
		r.packet(r.now, r.lan, browser, true, query(0, "GL-AR750S._mvd._tcp.local", typeSRV))
		if len(r.out) != 1 || r.out[0].to != browser {
			t.Fatalf("round %d: sent %+v", i, r.out)
		}
		m := parseReply(t, r.out[0].msg)
		if types(m.answers) != "SRV" || m.answers[0].ttl != 120 || m.answers[0].class != classIN|cacheFlush || m.id != 0 || m.questions != 0 {
			t.Errorf("round %d: %+v", i, m)
		}
	}
}

// Section 5.4: a question can ask for a unicast reply (QU). It gets one if
// the network has heard the answer lately (within a quarter of its TTL);
// otherwise the reply is multicast, to keep other devices' caches fresh.
func TestUnicastReplyAskedFor(t *testing.T) {
	r := newRig(svc) // just announced
	q := query(0, "GL-AR750S._mvd._tcp.local", typeSRV|QU)
	if out := r.ask(browser, q); len(out) != 1 || out[0].to != browser {
		t.Fatalf("just announced: %+v", out)
	}
	r.run(31 * time.Second) // over a quarter of the SRV's 120 seconds
	if out := r.ask(browser, q); len(out) != 1 || out[0].to != nil {
		t.Fatalf("not multicast lately: %+v", out)
	}
	if out := r.ask(browser, q); len(out) != 1 || out[0].to != browser {
		t.Fatalf("just multicast: %+v", out)
	}
	// The PTR lasts 75 minutes: a quarter is nearly 19.
	r.run(18 * time.Minute)
	if out := r.ask(browser, query(0, "_mvd._tcp.local", typePTR|QU)); len(out) != 1 || out[0].to != browser {
		t.Fatalf("PTR after 18 minutes: %+v", out)
	}
	r.run(time.Minute)
	if out := r.ask(browser, query(0, "_mvd._tcp.local", typePTR|QU)); len(out) != 1 || out[0].to != nil {
		t.Fatalf("PTR after 19 minutes: %+v", out)
	}
}

// A reply goes by unicast only if every question it answers asked for
// that. Questions for other services don't count either way.
func TestUnicastReplyNeedsEveryQuestion(t *testing.T) {
	for _, c := range []struct {
		name    string
		q       []byte
		unicast bool
	}{
		{"both QU", query(0, "GL-AR750S._mvd._tcp.local", typeSRV|QU, "GL-AR750S._mvd._tcp.local", typeTXT|QU), true},
		{"one QM", query(0, "GL-AR750S._mvd._tcp.local", typeSRV|QU, "GL-AR750S._mvd._tcp.local", typeTXT), false},
		{"another's QM", query(0, "GL-AR750S._mvd._tcp.local", typeSRV|QU, "_airplay._tcp.local", typePTR), true},
		{"another's QU", query(0, "GL-AR750S._mvd._tcp.local", typeSRV, "_airplay._tcp.local", typePTR|QU), false},
	} {
		r := newRig(svc)
		if out := r.ask(browser, c.q); len(out) != 1 || (out[0].to != nil) != c.unicast {
			t.Errorf("%s: %+v", c.name, out)
		}
	}
}

// sentAt runs the clock on by d, and says when each message was sent in
// that time, in milliseconds after start.
func (r *rig) sentAt(start time.Time, d time.Duration) (at []int, msgs []sent) {
	r.out = nil
	for end := r.now.Add(d); r.now.Before(end); r.run(time.Millisecond) {
		for range r.out[len(at):] {
			at = append(at, int(r.now.Sub(start).Milliseconds()))
		}
	}
	return at, r.out
}

func ints(a []int) string {
	return strings.Trim(strings.Join(strings.Fields(fmt.Sprint(a)), " "), "[]")
}

// RFC 6762 section 6: an answer other responders may be giving too (a
// PTR) waits 20 to 120 ms, so they don't all answer in the same instant.
// One only this service has goes at once.
func TestSharedAnswersWait(t *testing.T) {
	r := newRig(svc)
	r.run(time.Hour)
	start := r.now
	r.packet(r.now, r.lan, browser, false, query(0, "_mvd._tcp.local", typePTR))
	if at, _ := r.sentAt(start, time.Second); ints(at) != "70" { // 20 ms and half of 100: the rig's rand gives half
		t.Errorf("PTR sent at %v ms", at)
	}
	r.run(time.Second)
	r.out = nil
	r.packet(r.now, r.lan, browser, false, query(0, "GL-AR750S._mvd._tcp.local", typeSRV))
	if len(r.out) != 1 {
		t.Errorf("SRV: %d messages sent at once", len(r.out))
	}
}

// Section 6: a record isn't multicast more than once a second. The burst
// in a real capture: six queries for the PTR in a few seconds, each of
// which was answered.
func TestOncePerSecond(t *testing.T) {
	r := newRig(svc)
	r.run(time.Hour)
	start := r.now
	q := query(0, "_mvd._tcp.local", typePTR)
	var at []int
	for i := 0; i < 6; i++ {
		r.packet(r.now, r.lan, browser, false, q)
		a, _ := r.sentAt(start, 400*time.Millisecond)
		at = append(at, a...)
	}
	a, _ := r.sentAt(start, 2*time.Second)
	if at = append(at, a...); ints(at) != "70 1070 2070" {
		t.Errorf("sent at %v ms", at)
	}
}

// Questions that arrive while a reply is waiting are answered in the same
// message.
func TestRepliesMerge(t *testing.T) {
	r := newRig(svc)
	r.run(time.Hour)
	start := r.now
	r.packet(r.now, r.lan, browser, false, query(0, "_mvd._tcp.local", typePTR))
	r.run(10 * time.Millisecond)
	r.packet(r.now, r.lan, browser, false, query(0, "_services._dns-sd._udp.local", typePTR))
	r.run(10 * time.Millisecond)
	r.packet(r.now, r.lan, browser, false, query(0, "mvd-gl-ar750s.local", typeAAAA)) // there's none: an NSEC says so
	at, out := r.sentAt(start, time.Second)
	if ints(at) != "70" {
		t.Fatalf("sent at %v ms", at)
	}
	if m := parseReply(t, out[0].msg); types(m.answers) != "PTR PTR" || types(m.extra) != "SRV TXT A NSEC" {
		t.Errorf("answers %q, extra %q", types(m.answers), types(m.extra))
	}
	// And a record that's just gone out as an additional isn't sent again
	// in the same second, even as one.
	r.packet(r.now, r.lan, browser, false, query(0, "GL-AR750S._mvd._tcp.local", typeTXT))
	at, out = r.sentAt(start, 2*time.Second)
	if ints(at) != "1070" || types(parseReply(t, out[0].msg).answers) != "TXT" {
		t.Errorf("TXT sent at %v ms", at)
	}
}

// Section 7.2: a query with more known answers than fit says so (the TC
// bit) and sends the rest in further packets. The reply waits 400 to 500 ms
// for them.
func TestTruncatedQuery(t *testing.T) {
	q := query(0, "_mvd._tcp.local", typePTR)
	q[2] |= 0x02 // TC
	rest := func(r *rig) []byte { return knowing(query(0), r.engine, r.lan, recPTR, 4500) }

	r := newRig(svc)
	r.run(time.Hour)
	start := r.now
	r.packet(r.now, r.lan, browser, false, q)
	if at, _ := r.sentAt(start, time.Second); ints(at) != "450" {
		t.Errorf("no more known answers: sent at %v ms", at)
	}

	r = newRig(svc)
	r.run(time.Hour)
	start = r.now
	r.packet(r.now, r.lan, browser, false, q)
	r.run(100 * time.Millisecond)
	r.packet(r.now, r.lan, browser, false, rest(r))
	if at, _ := r.sentAt(start, time.Second); len(at) != 0 {
		t.Errorf("the PTR known in the next packet: sent at %v ms", at)
	}

	// Someone else's known answers aren't this asker's.
	r = newRig(svc)
	r.run(time.Hour)
	start = r.now
	r.packet(r.now, r.lan, browser, false, q)
	r.run(100 * time.Millisecond)
	r.packet(r.now, r.lan, &net.UDPAddr{IP: net.IPv4(192, 168, 8, 77), Port: 5353}, false, rest(r))
	if at, _ := r.sentAt(start, time.Second); ints(at) != "450" {
		t.Errorf("another device's known answers: sent at %v ms", at)
	}
}

// Nothing is sent, and no timer is wanted, while no one's asking.
func TestQuietWhenNotAsked(t *testing.T) {
	r := newRig(svc)
	if !r.next().IsZero() {
		t.Error("something's due, after the announcements")
	}
	r.ask(browser, query(0, "_mvd._tcp.local", typePTR))
	if !r.next().IsZero() || r.lan.waiting != 0 {
		t.Error("something's due, after a reply")
	}
}
