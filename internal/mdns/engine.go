package mdns

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"
)

// A network is one address of one interface the service is advertised on,
// in one family, and what the engine remembers about it. An interface with
// an IPv4 address and IPv6 is two: mDNS over each is a conversation of its
// own, with its own group. Both give the same answers.
type network struct {
	ifindex int
	name    string     // "br-lan"
	v6      bool       // over IPv6: ip is the interface's link-local address
	ip      net.IP     // the address packets go from here: 4 bytes, or 16
	net     *net.IPNet // IPv4: who's on it
	// The interface's addresses, for the host's A and AAAA records: a is
	// ip on an IPv4 network, and nil on an IPv6 one whose interface has
	// no IPv4.
	a    net.IP
	aaaa []net.IP

	phase phase
	step  int       // probes or announcements sent
	at    time.Time // when the next is due; zero when none is
	// When each record was last multicast here, by its bit's number.
	last [numRecords]time.Time
	// The reply waiting to be multicast here: its answers, the records
	// that say there's no answer (which go as additionals), and when it's
	// due (zero: nothing's waiting). more: it's waiting for more known
	// answers from this address, which sent a truncated query.
	waiting, waitingNot records
	due                 time.Time
	more                netip.Addr
}

func (n *network) same(o *network) bool {
	return n.ifindex == o.ifindex && n.name == o.name && n.v6 == o.v6 && n.ip.Equal(o.ip)
}

// has is the records there's something to put in here: no A without an
// IPv4 address, no AAAA without an IPv6 one.
func (n *network) has() records {
	all := ^records(0)
	if n.a == nil {
		all &^= recA
	}
	if len(n.aaaa) == 0 {
		all &^= recAAAA
	}
	return all
}

// What a network is doing.
type phase uint8

const (
	// Asking whether anyone has the service's names, three times, 250 ms
	// apart (RFC 6762 section 8.1). Nothing's answered meanwhile: the
	// names aren't this service's yet.
	probing    phase = iota
	announcing       // saying the service is here, twice, a second apart (section 8.3)
	running
)

// The engine decides what to send and when: it's given each packet that
// arrives, and told when time has passed, and sends through send. It has
// no sockets, clocks or goroutines of its own (the Responder has those),
// so it can be tested with made-up packets and times. Not safe for use
// from two goroutines at once.
type engine struct {
	svc             Service // as it's advertised: the names may have a number added
	typ, inst, host name    // its names, made when they change
	nets            []*network

	// The names asked for, and the number each now has, when another
	// service on the network had it first: 2 is "GL-AR750S (2)" and
	// mvd-gl-ar750s-2.
	instance0, host0 string
	instanceN, hostN int
	// Conflicts lately, and when the last was: after 15 with under ten
	// seconds between them, each new try waits five (section 8.1).
	conflicts  int
	conflicted time.Time

	// send sends msg on n: to one address, or to the group (to nil).
	send func(n *network, to *net.UDPAddr, msg []byte)
	logf func(format string, args ...any)
	rand func(d time.Duration) time.Duration // a random time under d
}

func newEngine(svc Service, send func(*network, *net.UDPAddr, []byte), logf func(string, ...any)) *engine {
	e := &engine{svc: svc, send: send, logf: logf, rand: rand.N[time.Duration],
		instance0: svc.Instance, host0: svc.Host, instanceN: 1, hostN: 1}
	e.typ = svc.typeName()
	e.name()
	return e
}

// name sets the service's names from the ones asked for and their numbers.
func (e *engine) name() {
	e.svc.Instance, e.svc.Host = e.instance0, e.host0
	if e.instanceN > 1 {
		suffix := " (" + strconv.Itoa(e.instanceN) + ")"
		e.svc.Instance = cut(e.instance0, 63-len(suffix)) + suffix
	}
	if e.hostN > 1 {
		suffix := "-" + strconv.Itoa(e.hostN)
		e.svc.Host = cut(e.host0, 63-len(suffix)) + suffix
	}
	e.inst, e.host = e.svc.instanceName(), e.svc.hostName()
}

// conflict is someone else on n's network using one of the service's
// names (which: one of its records). If the service was asking for the
// name, it's taken: the service takes the next one, and asks for that. If
// it had the name already, it asks for it again (section 9): whoever else
// has it will be asking too, one of the two gives way (contest), and then
// hears that it's taken.
func (e *engine) conflict(now time.Time, n *network, which records) {
	if n.phase == probing && n.step == 0 {
		// Not asked here yet, so this is no answer: perhaps the other
		// half of what set the asking off (the same packet over IPv6, or
		// on another interface). If the name's taken, asking will say so.
		return
	}
	if n.phase != probing {
		e.logf("mvd: Bonjour: something else on %s answers to this server's name: checking", n.name)
	} else if which&(recSRV|recTXT) != 0 {
		was := e.svc.Instance
		e.instanceN++
		e.name()
		e.logf("mvd: Bonjour: there's another %q on the network: this one is now %q", was, e.svc.Instance)
	} else {
		was := e.svc.Host
		e.hostN++
		e.name()
		e.logf("mvd: Bonjour: another machine is %s.local: this one is now %s.local", was, e.svc.Host)
	}
	if now.Sub(e.conflicted) > 10*time.Second {
		e.conflicts = 0
	}
	e.conflicts++
	e.conflicted = now
	wait := e.rand(250 * time.Millisecond)
	if e.conflicts >= 15 {
		wait = 5 * time.Second
	}
	for _, n := range e.nets {
		n.probe(now.Add(wait))
	}
}

// probe starts n asking for the names again, at at.
func (n *network) probe(at time.Time) {
	*n = network{ifindex: n.ifindex, name: n.name, v6: n.v6, ip: n.ip, net: n.net, a: n.a, aaaa: n.aaaa, phase: probing, at: at}
}

// setNetworks says which networks there are now. On one that's new, the
// service's names are asked for, then announced; one that's gone is
// forgotten.
func (e *engine) setNetworks(now time.Time, found []*network) {
	for i, f := range found {
		for _, n := range e.nets {
			if n.same(f) {
				n.net, n.a, n.aaaa = f.net, f.a, f.aaaa
				found[i] = n
			}
		}
		if n := found[i]; n == f {
			// After a random wait, so machines switched on together
			// don't all ask in the same instant.
			n.probe(now.Add(e.rand(250 * time.Millisecond)))
		}
	}
	e.nets = found
}

// next is when tick next has something to do; zero if nothing's waiting.
func (e *engine) next() time.Time {
	var t time.Time
	for _, n := range e.nets {
		for _, at := range [...]time.Time{n.at, n.due} {
			if !at.IsZero() && (t.IsZero() || at.Before(t)) {
				t = at
			}
		}
	}
	return t
}

// tick sends whatever's due.
func (e *engine) tick(now time.Time) {
	for _, n := range e.nets {
		if !n.at.IsZero() && !now.Before(n.at) {
			switch {
			case n.phase == probing && n.step < 3:
				e.sendProbe(n)
				n.step++
				n.at = now.Add(250 * time.Millisecond)
			case n.phase == probing:
				// No one objected: the names are this service's.
				e.logf("mvd: Bonjour: %q on %s (%s)", e.svc.Instance, n.name, n.ip)
				n.phase, n.step = announcing, 1
				e.announce(now, n, false)
				n.at = now.Add(time.Second)
			default:
				e.announce(now, n, false)
				n.phase, n.at = running, time.Time{}
			}
		}
		if !n.due.IsZero() && !now.Before(n.due) {
			e.flush(now, n)
		}
	}
}

// goodbye tells every network the service is going, so browsers drop it
// now rather than when it times out.
func (e *engine) goodbye(now time.Time) {
	for _, n := range e.nets {
		n.waiting, n.waitingNot, n.due = 0, 0, time.Time{}
		if n.phase != probing {
			e.announce(now, n, true)
		}
	}
}

// announce multicasts the unsolicited message that tells a network about
// the service; goodbye says it's going (every TTL 0).
func (e *engine) announce(now time.Time, n *network, goodbye bool) {
	which := (recPTR | recServices | recSRV | recTXT | recA | recAAAA) & n.has()
	w := newBuilder(0, 0x8400)
	how := asUsual
	if goodbye {
		how = asGoodbye
	}
	w.count(1, e.write(w, which, n, how))
	e.send(n, nil, w.b)
	if !goodbye {
		n.sent(now, which)
	}
}

// The records only this service may have, which it asks for before using.
const unique = recSRV | recTXT | recA | recAAAA

// sendProbe multicasts a probe: a question for any record of each of the
// service's names, with the records it means to use, for whoever else is
// asking to compare (section 8.2). The question asks for a multicast
// answer (QM): the RFC prefers QU, but a unicast answer to port 5353 may
// be given to the system's responder, which shares the port, and never
// seen.
func (e *engine) sendProbe(n *network) {
	w := newBuilder(0, 0)
	for _, nm := range [...]name{e.inst, e.host} {
		w.name(nm)
		w.b = append(w.b, 0, typeANY, 0, classIN)
	}
	w.count(0, 2)
	w.count(2, e.write(w, unique, n, asProbe))
	e.send(n, nil, w.b)
}

// sent notes that these records were multicast now.
func (n *network) sent(now time.Time, which records) {
	for i := range n.last {
		if which&(1<<i) != 0 {
			n.last[i] = now
		}
	}
}

// recent says whether every one of these records was multicast here within
// a quarter of its TTL: other devices' caches are fresh enough that a
// reply can go to the asker alone (RFC 6762 section 5.4).
func (n *network) recent(now time.Time, which records) bool {
	for i := range n.last {
		r := records(1) << i
		if which&r != 0 && (n.last[i].IsZero() || now.Sub(n.last[i]) > time.Duration(r.ttl())*time.Second/4) {
			return false
		}
	}
	return true
}

// packet handles a message that arrived on n from from. direct: it was
// addressed to this machine alone, not to the group.
func (e *engine) packet(now time.Time, n *network, from *net.UDPAddr, direct bool, msg []byte) {
	h, ok := readHeader(msg)
	switch {
	case !ok || h.flags&0x7800 != 0: // not a standard query, or the answer to one
	case !h.response():
		e.query(now, n, from, direct, msg, h)
	case from.Port == 5353:
		e.heard(now, n, msg, h)
	}
}

// heard looks through someone's response for a record with one of the
// service's names and other data: a conflict (section 9). The same data
// is no conflict: it's this service's own message come back, or another
// of this machine's interfaces on the same network.
func (e *engine) heard(now time.Time, n *network, msg []byte, h header) {
	off, ok := skipQuestions(msg, h)
	if !ok {
		return
	}
	for i := 0; i < min(h.an+h.ns+h.ar, maxRecords); i++ {
		r, end, err := readRecord(msg, off)
		if err != nil {
			return
		}
		which, same := e.own(msg, r, n)
		if which&unique != 0 && !same && r.ttl != 0 && !(which&(recA|recAAAA) != 0 && e.mine(msg[r.data:r.end])) {
			e.conflict(now, n, which)
			return
		}
		off = end
	}
}

// skipQuestions returns the offset past a message's questions; not ok if
// they can't be read, or there are too many to bother with.
func skipQuestions(msg []byte, h header) (off int, ok bool) {
	if h.qd > maxQuestions {
		return 0, false
	}
	off = 12
	for i := 0; i < h.qd; i++ {
		end, err := skipName(msg, off)
		if err != nil || end+4 > len(msg) {
			return 0, false
		}
		off = end + 4
	}
	return off, true
}

// mine says whether an address is one of this machine's, on any network
// the service is on.
func (e *engine) mine(ip []byte) bool {
	for _, n := range e.nets {
		if n.a.Equal(ip) || slices.ContainsFunc(n.aaaa, func(a net.IP) bool { return a.Equal(ip) }) {
			return true
		}
	}
	return false
}

// A record as it's compared when two are asking for the same name: its
// type, and its data with names written out in full.
type rival struct {
	rtype uint16
	data  []byte
}

// contest compares the records a probe in msg proposes (in its authority
// section, at off) with this service's, for the instance's name and for
// the host's. same: the records of this service's that the probe has just
// as they are (it's this service's own, heard back). lose: for one of the
// names, this service's records are the lower, and it gives way (section
// 8.2). Lower means: sorted by type then data, the first record that
// differs is lower, or there are fewer.
func (e *engine) contest(msg []byte, off, count int, n *network) (same records, lose bool) {
	var theirs, theirHost []rival
	strangers := false // a host address that isn't this machine's
	for i := 0; i < min(count, maxRecords); i++ {
		r, end, err := readRecord(msg, off)
		if err != nil {
			return 0, false
		}
		off = end
		if r.class&^cacheFlush != classIN {
			continue
		}
		data := msg[r.data:r.end]
		switch {
		case nameAt(msg, r.name, e.inst):
			if r.rtype == typeSRV && len(data) > 6 {
				// The target may be compressed: written out, as ours is.
				target, _, err := readName(msg, r.data+6)
				if err != nil {
					return 0, false
				}
				w := &builder{b: append([]byte(nil), data[:6]...)}
				w.name(target)
				data = w.b
			}
			theirs = append(theirs, rival{r.rtype, data})
		case nameAt(msg, r.name, e.host):
			theirHost = append(theirHost, rival{r.rtype, data})
			strangers = strangers || !e.mine(data)
		}
	}
	if theirs != nil {
		switch c := compare(e.rivals(recSRV|recTXT, n), theirs); {
		case c == 0:
			same |= recSRV | recTXT
		case c < 0:
			lose = true
		}
	}
	switch {
	case theirHost == nil:
	case !strangers: // another of this machine's interfaces, or another server on it
		same |= recA
	case compare(e.rivals(recA|recAAAA, n), theirHost) < 0:
		lose = true
	}
	return same, lose
}

// rivals is this service's records, as contest compares them.
func (e *engine) rivals(which records, n *network) []rival {
	w := &builder{b: make([]byte, 12)} // no names map: nothing's compressed
	e.write(w, which, n, asProbe)
	var out []rival
	for off := 12; off < len(w.b); {
		r, end, _ := readRecord(w.b, off)
		out = append(out, rival{r.rtype, w.b[r.data:r.end]})
		off = end
	}
	return out
}

func compare(a, b []rival) int {
	order := func(x, y rival) int {
		if c := cmp.Compare(x.rtype, y.rtype); c != 0 {
			return c
		}
		return bytes.Compare(x.data, y.data)
	}
	slices.SortFunc(a, order)
	slices.SortFunc(b, order)
	return slices.CompareFunc(a, b, order)
}

// query answers a query, if any of it is for this service.
func (e *engine) query(now time.Time, n *network, from *net.UDPAddr, direct bool, msg []byte, h header) {
	if n.phase == probing {
		// Nothing to answer with yet. But if it's someone else asking for
		// the same names at the same moment, one of the two gives way:
		// the one whose records are lower waits a second and asks again,
		// by which time the other has the name and says so.
		if off, ok := skipQuestions(msg, h); ok && h.an == 0 && h.ns > 0 && from.Port == 5353 {
			if _, lose := e.contest(msg, off, h.ns, n); lose {
				n.probe(now.Add(time.Second))
			}
		}
		return
	}
	var answers, negative records
	unicast := true // every question that's answered asked for the reply by unicast (QU)
	off, nq := 12, min(h.qd, maxQuestions)
	for i := 0; i < nq; i++ {
		end, err := skipName(msg, off)
		if err != nil || end+4 > len(msg) {
			return
		}
		a, x := e.asked(msg, off, binary.BigEndian.Uint16(msg[end:]), n)
		if a|x != 0 && binary.BigEndian.Uint16(msg[end+2:])&0x8000 == 0 {
			unicast = false
		}
		answers |= a
		negative |= x
		off = end + 4
	}
	questions := msg[12:off]
	// More of a truncated query's known answers, from whoever sent it.
	sender := from.AddrPort().Addr().Unmap()
	more := n.due != (time.Time{}) && n.more == sender
	if answers|negative == 0 && !more {
		return
	}
	// The answers the asker has already, listed after its questions: one
	// with at least half its TTL left isn't sent again (RFC 6762 section
	// 7.1).
	if h.qd <= maxQuestions {
		addresses := 0 // of the host's AAAA records, how many are known
		for i := 0; i < min(h.an, maxRecords); i++ {
			r, end, err := readRecord(msg, off)
			if err != nil {
				return
			}
			off = end
			which, same := e.own(msg, r, n)
			if !same || r.ttl < which.ttl()/2 {
				continue
			}
			if which == recAAAA {
				if addresses++; addresses < len(n.aaaa) {
					continue
				}
			}
			answers &^= which
			if more {
				n.waiting &^= which
			}
		}
	}
	if more && n.waiting|n.waitingNot == 0 {
		n.due, n.more = time.Time{}, netip.Addr{}
	}
	// A probe: someone asking whether a name is taken, with the records
	// they'd use. If they're this service's own (its probe on another of
	// this machine's interfaces), there's nothing to defend.
	probe := h.qd <= maxQuestions && h.an <= maxRecords && h.ns > 0
	if probe {
		same, _ := e.contest(msg, off, h.ns, n)
		answers &^= same
		if same&recSRV != 0 {
			negative &^= recNSECInstance
		}
		if same&recA != 0 {
			negative &^= recNSEC
		}
	}
	extra := (negative | n.with(answers)) &^ answers
	if answers|extra == 0 {
		return
	}

	// A query not from port 5353 is a one-shot (legacy) resolver's: the
	// reply goes straight back to it, echoing its id and questions.
	if from.Port != 5353 {
		w := newBuilder(h.id, 0x8400)
		w.count(0, nq)
		w.b = append(w.b, questions...) // no names recorded for compression: fine, it's only lost savings
		w.count(1, e.write(w, answers, n, asLegacy))
		w.count(3, e.write(w, extra, n, asLegacy))
		e.send(n, from, w.b)
		return
	}
	// To the asker alone if it asked this machine alone (section 5.5), or
	// asked for that and the network has heard these answers lately
	// (section 5.4). Otherwise to everyone.
	if direct || unicast && n.recent(now, answers) {
		e.reply(n, from, answers, extra)
		return
	}
	// A probe is answered at once, however lately the records went out:
	// it has only 250 ms to hear that the name's taken.
	if probe {
		e.reply(n, nil, answers, extra)
		n.sent(now, answers|extra)
		return
	}
	// To everyone, with whatever else is waiting to go. At once if every
	// answer is a record only this service has. After 20 to 120 ms if
	// others may be answering the same question (a PTR), so they don't
	// all answer in the same instant (section 6). After 400 to 500 ms if
	// the query was truncated: more of its known answers are on their way
	// (section 7.2).
	due := now
	if answers&(recPTR|recServices) != 0 {
		due = now.Add(20*time.Millisecond + e.rand(100*time.Millisecond))
	}
	if h.truncated() {
		due = now.Add(400*time.Millisecond + e.rand(100*time.Millisecond))
	}
	// A reply already waiting isn't hurried by this one: they go together.
	if n.due.IsZero() || h.truncated() && due.After(n.due) {
		n.due = due
	}
	if h.truncated() {
		n.more = sender
	}
	n.waiting |= answers
	n.waitingNot |= negative
	if !n.due.After(now) {
		e.flush(now, n)
	}
}

// reply sends a response to the group (to nil), or to one address.
func (e *engine) reply(n *network, to *net.UDPAddr, answers, extra records) {
	w := newBuilder(0, 0x8400) // a response, authoritative
	w.count(1, e.write(w, answers, n, asUsual))
	w.count(3, e.write(w, extra, n, asUsual))
	e.send(n, to, w.b)
}

// flush multicasts the reply that's waiting on n. A record isn't multicast
// more than once a second (RFC 6762 section 6): an answer sent that
// lately stays waiting until its second is up, and an additional is left
// out.
func (e *engine) flush(now time.Time, n *network) {
	var ready records
	var later time.Time
	for i := range n.last {
		r := records(1) << i
		if n.waiting&r == 0 {
			continue
		}
		if again := n.last[i].Add(time.Second); n.last[i].IsZero() || !now.Before(again) {
			ready |= r
		} else if later.IsZero() || again.Before(later) {
			later = again
		}
	}
	extra := (n.waitingNot | n.with(ready)) &^ ready
	for i := range n.last {
		if !n.last[i].IsZero() && now.Sub(n.last[i]) < time.Second {
			extra &^= 1 << i
		}
	}
	if ready|extra != 0 {
		e.reply(n, nil, ready, extra)
		n.sent(now, ready|extra)
	}
	n.waiting &^= ready
	n.waitingNot = 0
	n.due = later
	if later.IsZero() {
		n.more = netip.Addr{}
	}
}

// asked says which records answer a question for the name at off, and
// which say there's no such record: an NSEC, listing what the name does
// have (the host's, for an AAAA).
func (e *engine) asked(msg []byte, off int, qtype uint16, n *network) (answers, negative records) {
	is := func(t uint16) bool { return qtype == t || qtype == typeANY }
	switch {
	case nameAt(msg, off, e.typ):
		if is(typePTR) {
			answers = recPTR
		}
	case nameAt(msg, off, servicesName):
		if is(typePTR) {
			answers = recServices
		}
	case nameAt(msg, off, e.inst):
		if is(typeSRV) {
			answers |= recSRV
		}
		if is(typeTXT) {
			answers |= recTXT
		}
		if answers == 0 {
			negative = recNSECInstance
		}
	case nameAt(msg, off, e.host):
		if is(typeA) {
			answers |= recA
		}
		if is(typeAAAA) {
			answers |= recAAAA
		}
		if answers &= n.has(); answers == 0 || qtype == typeANY {
			negative = recNSEC
		}
	}
	return answers, negative
}

// with is the records that go with these answers unasked, as additionals:
// what the asker will want next. With either kind of address goes the
// other, and the NSEC that says there are no more.
func (n *network) with(answers records) (extra records) {
	if answers&recPTR != 0 {
		extra |= recSRV | recTXT
	}
	if (answers|extra)&recSRV != 0 {
		extra |= recA | recAAAA
	}
	if (answers|extra)&(recA|recAAAA) != 0 {
		extra |= recA | recAAAA | recNSEC
	}
	return extra & n.has()
}

// own says which of the service's records r is, going by its name and
// type (none: it's someone else's), and whether its data is the same.
func (e *engine) own(msg []byte, r record, n *network) (which records, same bool) {
	if r.class&^cacheFlush != classIN {
		return 0, false
	}
	data := msg[r.data:r.end]
	switch r.rtype {
	case typePTR:
		switch {
		case nameAt(msg, r.name, e.typ):
			return recPTR, nameAt(msg, r.data, e.inst)
		case nameAt(msg, r.name, servicesName):
			return recServices, nameAt(msg, r.data, e.typ)
		}
	case typeSRV:
		if nameAt(msg, r.name, e.inst) {
			return recSRV, len(data) > 6 && data[0]|data[1]|data[2]|data[3] == 0 &&
				int(binary.BigEndian.Uint16(data[4:])) == e.svc.Port && nameAt(msg, r.data+6, e.host)
		}
	case typeTXT:
		if nameAt(msg, r.name, e.inst) {
			return recTXT, e.txtIs(data)
		}
	case typeA:
		if nameAt(msg, r.name, e.host) {
			return recA, len(data) == 4 && n.a.Equal(data)
		}
	case typeAAAA:
		if nameAt(msg, r.name, e.host) {
			return recAAAA, len(data) == 16 && slices.ContainsFunc(n.aaaa, func(a net.IP) bool { return a.Equal(data) })
		}
	}
	return 0, false
}

// txtIs says whether data is the service's TXT record's.
func (e *engine) txtIs(data []byte) bool {
	if len(e.svc.TXT) == 0 {
		return len(data) == 1 && data[0] == 0
	}
	for _, t := range e.svc.TXT {
		t = t[:min(len(t), 255)]
		if len(data) < 1+len(t) || int(data[0]) != len(t) || string(data[1:1+len(t)]) != t {
			return false
		}
		data = data[1+len(t):]
	}
	return len(data) == 0
}

// How records are written.
type how uint8

const (
	asUsual   how = iota
	asLegacy      // to a one-shot resolver: short TTLs, and no cache-flush bit, which it wouldn't understand
	asProbe       // in a probe: no cache-flush bit, as the records aren't this service's yet
	asGoodbye     // every TTL 0
)

// write writes the records in which, in a fixed order, and says how many.
func (e *engine) write(w *builder, which records, n *network, how how) int {
	ttl := func(r records) uint32 {
		switch how {
		case asGoodbye:
			return 0
		case asLegacy:
			return min(r.ttl(), legacyTTL)
		}
		return r.ttl()
	}
	// Records only this host has (all but the PTRs) tell caches to drop
	// anything else they hold for the name.
	unique := uint16(classIN | cacheFlush)
	if how == asLegacy || how == asProbe {
		unique = classIN
	}
	s := &e.svc
	c := 0
	if which&recPTR != 0 {
		w.record(e.typ, typePTR, classIN, ttl(recPTR), func() { w.name(e.inst) })
		c++
	}
	if which&recServices != 0 {
		w.record(servicesName, typePTR, classIN, ttl(recServices), func() { w.name(e.typ) })
		c++
	}
	if which&recSRV != 0 {
		w.record(e.inst, typeSRV, unique, ttl(recSRV), func() {
			w.b = append(w.b, 0, 0, 0, 0, byte(s.Port>>8), byte(s.Port)) // priority, weight, port
			w.name(e.host)
		})
		c++
	}
	if which&recTXT != 0 {
		w.record(e.inst, typeTXT, unique, ttl(recTXT), func() {
			if len(s.TXT) == 0 {
				w.b = append(w.b, 0) // never empty: one empty string
			}
			for _, t := range s.TXT {
				t = t[:min(len(t), 255)]
				w.b = append(w.b, byte(len(t)))
				w.b = append(w.b, t...)
			}
		})
		c++
	}
	if which&recNSECInstance != 0 {
		w.record(e.inst, typeNSEC, unique, ttl(recNSECInstance), func() {
			// As the host's, below. TXT is type 16 and SRV 33: the first
			// bit of the third byte and the second of the fifth.
			w.plain(e.inst)
			w.b = append(w.b, 0, 5, 0, 0, 0x80, 0, 0x40)
		})
		c++
	}
	if which&recA != 0 && n.a != nil {
		w.record(e.host, typeA, unique, ttl(recA), func() { w.b = append(w.b, n.a.To4()...) })
		c++
	}
	if which&recAAAA != 0 {
		for _, ip := range n.aaaa {
			w.record(e.host, typeAAAA, unique, ttl(recAAAA), func() { w.b = append(w.b, ip.To16()...) })
			c++
		}
	}
	if which&recNSEC != 0 {
		w.record(e.host, typeNSEC, unique, ttl(recNSEC), func() {
			// The next name is the host's own, written out (RFC 6762 section
			// 18.14 forbids compressing it), then window 0's types, a bit
			// each: A is type 1, the second bit of the first byte, and
			// AAAA 28, the fifth of the fourth.
			w.plain(e.host)
			var a byte
			if n.a != nil {
				a = 0x40
			}
			if len(n.aaaa) > 0 {
				w.b = append(w.b, 0, 4, a, 0, 0, 0x08)
			} else {
				w.b = append(w.b, 0, 1, a)
			}
		})
		c++
	}
	return c
}
