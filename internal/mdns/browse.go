package mdns

import (
	"encoding/binary"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// Found is a service that Browse found.
type Found struct {
	Name string   // the instance: "GL-AR750S"
	Host string   // "mvd-gl-ar750s.local"
	IP   net.IP   // an IPv4 address it answered with, or from
	Port int      // 0 if it never said
	TXT  []string // "key=value"
}

// Browse looks for services of a type ("_mvd._tcp") on the local network
// for wait, and returns what answered, in name order.
//
// It asks as a one-shot resolver does (RFC 6762 section 5.1): a question
// to the group from an ordinary port, on each private network. Responders
// answer that straight back, with everything in one reply, so there's no
// port to share, no group to join and no cache to keep. IPv4 only.
func Browse(service string, wait time.Duration) ([]Found, error) {
	nets := list(interfaces(), nil, true, false)
	b := &browse{typ: append(strings.Split(service, "."), "local")}
	var conns []*net.UDPConn
	var err error
	for _, n := range nets {
		// From the network's own address: Linux and Windows send a
		// socket's multicast out of the interface it's bound to. The BSDs
		// and macOS need telling, and where they can't be, never mind.
		c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: n.ip})
		if e != nil {
			err = e
			continue
		}
		defer c.Close()
		control(c, func(fd uintptr) error { return multicastInterface(fd, n.ip) })
		conns = append(conns, c)
	}
	if len(conns) == 0 {
		return nil, err // nil with no private network at all: nothing's found
	}
	id := uint16(1 + rand.N(0xFFFF))
	ask := func(q []byte) {
		for _, c := range conns {
			c.WriteToUDP(q, group)
		}
	}
	ask(b.question(id))

	// Part way through, ask once more for whatever a reply left out: a
	// responder that answers a browse with the PTR alone.
	again := time.AfterFunc(wait/3, func() {
		b.mu.Lock()
		q := b.rest(id)
		b.mu.Unlock()
		if q != nil {
			ask(q)
		}
	})
	defer again.Stop()

	deadline := time.Now().Add(wait)
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 9000)
			c.SetReadDeadline(deadline)
			for {
				n, from, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				b.mu.Lock()
				b.heard(buf[:n], id, from.IP)
				b.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.found(), nil
}

// A browse is what's been heard so far.
type browse struct {
	typ  name
	mu   sync.Mutex
	seen []*seen
	a    []address
}

type seen struct {
	name   name // the instance's
	host   name
	port   int
	txt    []string
	hasTXT bool
	from   net.IP // who said so
}

type address struct {
	host name
	ip   net.IP
}

// question is a query for the type's PTR; rest is one for the SRV, TXT
// and address of whatever's been heard of without them, or nil if nothing
// lacks any.
func (b *browse) question(id uint16) []byte {
	w := newBuilder(id, 0)
	w.name(b.typ)
	w.b = append(w.b, 0, typePTR, 0, classIN)
	w.count(0, 1)
	return w.b
}

func (b *browse) rest(id uint16) []byte {
	w := newBuilder(id, 0)
	n := 0
	ask := func(nm name, qtype byte) {
		if n < maxQuestions {
			w.name(nm)
			w.b = append(w.b, 0, qtype, 0, classIN)
			n++
		}
	}
	for _, s := range b.seen {
		if s.host == nil {
			ask(s.name, typeSRV)
		} else if b.addressOf(s.host) == nil {
			ask(s.host, typeA)
		}
		if !s.hasTXT {
			ask(s.name, typeTXT)
		}
	}
	if n == 0 {
		return nil
	}
	w.count(0, n)
	return w.b
}

func (b *browse) addressOf(host name) net.IP {
	if len(host) == 0 {
		return nil
	}
	for _, a := range b.a {
		if a.host.equal(host) {
			return a.ip
		}
	}
	return nil
}

// instance finds, or adds, the record of an instance of the type; nil if
// nm isn't one.
func (b *browse) instance(nm name, from net.IP) *seen {
	if len(nm) != len(b.typ)+1 || !nm[1:].equal(b.typ) {
		return nil
	}
	for _, s := range b.seen {
		if s.name.equal(nm) {
			return s
		}
	}
	if len(b.seen) >= maxRecords { // anyone on the LAN can answer: not without end
		return nil
	}
	s := &seen{name: nm, from: from}
	b.seen = append(b.seen, s)
	return s
}

// heard takes in a reply to the query with this id.
func (b *browse) heard(msg []byte, id uint16, from net.IP) {
	h, ok := readHeader(msg)
	if !ok || !h.response() || h.id != id {
		return
	}
	off, ok := skipQuestions(msg, h)
	if !ok {
		return
	}
	for i := 0; i < min(h.an+h.ns+h.ar, maxRecords); i++ {
		r, end, err := readRecord(msg, off)
		if err != nil {
			return
		}
		off = end
		if r.class&^cacheFlush != classIN {
			continue
		}
		nm, _, err := readName(msg, r.name)
		if err != nil {
			return
		}
		data := msg[r.data:r.end]
		switch r.rtype {
		case typePTR:
			if nm.equal(b.typ) {
				if target, _, err := readName(msg, r.data); err == nil {
					b.instance(target, from)
				}
			}
		case typeSRV:
			if s := b.instance(nm, from); s != nil && len(data) > 6 {
				if host, _, err := readName(msg, r.data+6); err == nil {
					s.host, s.port = host, int(binary.BigEndian.Uint16(data[4:]))
				}
			}
		case typeTXT:
			if s := b.instance(nm, from); s != nil {
				s.txt, s.hasTXT = nil, true
				for len(data) > 0 && int(data[0]) < len(data) {
					if data[0] > 0 {
						s.txt = append(s.txt, string(data[1:1+data[0]]))
					}
					data = data[1+data[0]:]
				}
			}
		case typeA:
			if len(data) == 4 && b.addressOf(nm) == nil && len(b.a) < maxRecords {
				b.a = append(b.a, address{nm, net.IP(slices.Clone(data))})
			}
		}
	}
}

// found is what's been heard of, in name order. A service whose host gave
// no address is at the address its reply came from.
func (b *browse) found() []Found {
	var out []Found
	for _, s := range b.seen {
		f := Found{Name: s.name[0], Host: strings.Join(s.host, "."), IP: b.addressOf(s.host), Port: s.port, TXT: s.txt}
		if f.IP == nil {
			f.IP = s.from
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(x, y Found) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	return out
}
