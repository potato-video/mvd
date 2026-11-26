package mdns

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func show(found []Found) string {
	var s []string
	for _, f := range found {
		s = append(s, fmt.Sprintf("%s %s %s:%d %s", f.Name, f.Host, f.IP, f.Port, strings.Join(f.TXT, ",")))
	}
	return strings.Join(s, "\n")
}

// oneShot is a server's reply to a browse's question, as it arrives: from
// the server's address, to the one-shot resolver's port.
func oneShot(t *testing.T, s Service, ip string, q []byte) (msg []byte, from net.IP) {
	t.Helper()
	w := newWire()
	h := w.add(s, ip)
	w.run(5 * time.Second)
	h.out = nil
	h.packet(w.now, h.n, resolver, false, q)
	if len(h.out) != 1 || h.out[0].to != resolver {
		t.Fatalf("%s sent %+v", ip, h.out)
	}
	return h.out[0].msg, h.n.ip
}

func TestBrowseQuestion(t *testing.T) {
	b := &browse{typ: name{"_mvd", "_tcp", "local"}}
	q := b.question(0x1234)
	if string(q) != "\x12\x34\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x04_mvd\x04_tcp\x05local\x00\x00\x0c\x00\x01" {
		t.Errorf("%q", q)
	}
}

// What the servers answer a one-shot question with is all a browse needs.
func TestBrowseHears(t *testing.T) {
	b := &browse{typ: name{"_mvd", "_tcp", "local"}}
	q := b.question(7)
	other := Service{Instance: "MBP14", Type: "_mvd._tcp", Host: "mvd-mbp14", Port: 6838}
	for i := 0; i < 2; i++ { // each heard twice: listed once
		msg, from := oneShot(t, svc, "192.168.8.1", q)
		b.heard(msg, 7, from)
		msg, from = oneShot(t, other, "192.168.8.219", q)
		b.heard(msg, 7, from)
	}
	// Not replies to this question: another's, and a question.
	msg, from := oneShot(t, Service{Instance: "Stray", Type: "_mvd._tcp", Host: "stray", Port: 1}, "192.168.8.3", b.question(8))
	b.heard(msg, 7, from)
	b.heard(q, 7, from)
	if got, want := show(b.found()), `GL-AR750S mvd-gl-ar750s.local 192.168.8.1:6837 version=v1.2,auth=token
MBP14 mvd-mbp14.local 192.168.8.219:6838 `; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if b.rest(7) != nil {
		t.Error("asking for more, with everything heard")
	}
}

// A responder that answers with the PTR alone is asked for the rest, and
// one that never gives an address is at the address it answered from.
func TestBrowseAsksForTheRest(t *testing.T) {
	b := &browse{typ: name{"_mvd", "_tcp", "local"}}
	r := newRig(svc)
	reply := func(which records) []byte {
		w := newBuilder(7, 0x8400)
		w.count(1, r.write(w, which, r.lan, asLegacy))
		return w.b
	}
	from := net.IPv4(192, 168, 8, 77)
	b.heard(reply(recPTR), 7, from)
	questions := func(q []byte) string {
		m := parseQuestions(t, q)
		return strings.Join(m, "; ")
	}
	if got := questions(b.rest(7)); got != "GL-AR750S._mvd._tcp.local SRV; GL-AR750S._mvd._tcp.local TXT" {
		t.Errorf("after the PTR: %s", got)
	}
	b.heard(reply(recSRV|recTXT), 7, from)
	if got := questions(b.rest(7)); got != "mvd-gl-ar750s.local A" {
		t.Errorf("after the SRV: %s", got)
	}
	if got := show(b.found()); got != "GL-AR750S mvd-gl-ar750s.local 192.168.8.77:6837 version=v1.2,auth=token" {
		t.Errorf("no address: %s", got)
	}
	b.heard(reply(recA), 7, from)
	if got := show(b.found()); got != "GL-AR750S mvd-gl-ar750s.local 192.168.8.1:6837 version=v1.2,auth=token" || b.rest(7) != nil {
		t.Errorf("with the address: %s", got)
	}
}

// parseQuestions lists a query's questions: "name TYPE".
func parseQuestions(t *testing.T, q []byte) (out []string) {
	t.Helper()
	if len(q) < 12 {
		t.Fatalf("no query: %q", q)
	}
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(q[4:])); i++ {
		n, end, err := readName(q, off)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, dotted(n)+" "+map[uint16]string{typeA: "A", typeSRV: "SRV", typeTXT: "TXT", typePTR: "PTR"}[binary.BigEndian.Uint16(q[end:])])
		off = end + 4
	}
	return out
}

func TestBrowseBadReplies(t *testing.T) {
	b := &browse{typ: name{"_mvd", "_tcp", "local"}}
	msg, from := oneShot(t, svc, "192.168.8.1", b.question(7))
	for i := range msg {
		b.heard(msg[:i], 7, from) // mustn't panic
	}
}

// The real thing: a responder on this machine's network, found by a
// browse, through real sockets. Only when asked for (MVD_TEST_NETWORK=1):
// it needs a network that carries multicast, announces a made-up service
// on it for a few seconds, and on a Mac the firewall asks about it.
func TestLiveBrowse(t *testing.T) {
	if os.Getenv("MVD_TEST_NETWORK") == "" {
		t.Skip("set MVD_TEST_NETWORK=1 to try the real network")
	}
	anyAddress = true
	defer func() { anyAddress = false }()
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	s := Service{Instance: "mvd's test", Type: "_mvd-test._tcp", Host: "mvd-test", Port: 6837, TXT: []string{"version=test"}}
	a, err := Start(s, nil, logf)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s.Port = 6838
	b, err := Start(s, nil, logf) // a second, with the same name: it takes another
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	time.Sleep(4 * time.Second) // asking, giving way, asking again
	found, err := Browse("_mvd-test._tcp", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("found:\n%s", show(found))
	if len(found) != 2 || found[0].Name != "mvd's test" || found[1].Name != "mvd's test (2)" ||
		found[0].Port+found[1].Port != 6837+6838 || found[0].IP == nil || found[0].Host != "mvd-test.local" {
		t.Errorf("found %+v", found)
	}
}

// A responder whose sockets stop working is found again: it tries them
// now and then, and opens new ones if a send fails. (On a Mac, the local
// network privacy rules changing under a socket, as installing an app in a
// simulator does, can leave it refusing every send, and deaf, for good.)
// Here they're closed from under it. Only when asked for
// (MVD_TEST_NETWORK=1), as above.
func TestLiveReopen(t *testing.T) {
	if os.Getenv("MVD_TEST_NETWORK") == "" {
		t.Skip("set MVD_TEST_NETWORK=1 to try the real network")
	}
	anyAddress = true
	defer func() { anyAddress = false }()
	every := checkEvery
	checkEvery = time.Second
	defer func() { checkEvery = every }()
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	s := Service{Instance: "mvd's reopen test", Type: "_mvd-test._tcp", Host: "mvd-test", Port: 6839}
	r, err := Start(s, nil, logf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	time.Sleep(2 * time.Second) // asking, announcing
	r.mu.Lock()
	for _, c := range [...]*net.UDPConn{r.conn4, r.conn6} {
		if c != nil {
			c.Close()
		}
	}
	r.mu.Unlock()
	time.Sleep(4 * time.Second) // tried, opened again, asking, announcing
	found, err := Browse("_mvd-test._tcp", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != s.Instance {
		t.Errorf("found %+v", found)
	}
}
