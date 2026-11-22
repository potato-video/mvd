// Package mdns advertises one service over Multicast DNS (RFC 6762) with
// DNS-SD (RFC 6763): what Apple calls Bonjour. It's a small responder: it
// answers for its own names and ignores everything else.
package mdns

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Record types and classes.
const (
	typeA    = 1
	typePTR  = 12
	typeTXT  = 16
	typeAAAA = 28
	typeSRV  = 33
	typeNSEC = 47
	typeANY  = 255

	classIN    = 1
	cacheFlush = 0x8000 // on a record's class: it replaces whatever caches hold for the name
)

// A name is its labels: "_mvd", "_tcp", "local". A label can hold dots
// (an instance name like "Warren's TV.mvd" is one label), so names aren't
// kept as dotted strings.
type name []string

func (n name) equal(o name) bool {
	if len(n) != len(o) {
		return false
	}
	for i := range n {
		if !strings.EqualFold(n[i], o[i]) {
			return false
		}
	}
	return true
}

var errBad = errors.New("mdns: malformed message")

// readName reads the name at off, following compression pointers, and
// returns it with the offset just past it in the message.
func readName(msg []byte, off int) (name, int, error) {
	var n name
	end, size := -1, 0
	for steps := 0; steps < 128; steps++ { // a name has at most 127 labels; more is a pointer loop
		if off >= len(msg) {
			return nil, 0, errBad
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return n, end, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return nil, 0, errBad
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
		case l&0xC0 != 0:
			return nil, 0, errBad
		default:
			if size += 1 + l; off+1+l > len(msg) || size > 255 { // a name is at most 255 bytes
				return nil, 0, errBad
			}
			n = append(n, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
	return nil, 0, errBad
}

// A header is a message's first 12 bytes.
type header struct {
	id, flags      uint16
	qd, an, ns, ar int // how many questions, answers, authority and additional records
}

func (h header) response() bool { return h.flags&0x8000 != 0 }

// truncated: more of the query's known answers follow in another packet.
func (h header) truncated() bool { return h.flags&0x0200 != 0 }

func readHeader(msg []byte) (h header, ok bool) {
	if len(msg) < 12 {
		return h, false
	}
	u := func(i int) int { return int(binary.BigEndian.Uint16(msg[i:])) }
	return header{uint16(u(0)), uint16(u(2)), u(4), u(6), u(8), u(10)}, true
}

// Anyone on the LAN can send packets as fast as they like, and every
// device's mDNS traffic arrives here, so a packet mustn't cost much: this
// many questions and records are looked at, names are compared where they
// lie (nameAt) rather than built, and a record is only decoded when its
// name is one of the service's.
const (
	maxQuestions = 32
	maxRecords   = 64
)

// skipName returns the offset just past the name at off.
func skipName(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errBad
		}
		l := int(msg[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return 0, errBad
			}
			return off + 2, nil
		case l&0xC0 != 0:
			return 0, errBad
		}
		off += 1 + l
	}
}

// nameAt says whether the name at off is n, following compression
// pointers and ignoring ASCII case (RFC 6762 section 16), without building
// it.
func nameAt(msg []byte, off int, n name) bool {
	i := 0
	for steps := 0; steps < 128; steps++ {
		if off >= len(msg) {
			return false
		}
		l := int(msg[off])
		switch {
		case l == 0:
			return i == len(n)
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return false
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
		case l&0xC0 != 0:
			return false
		default:
			if off+1+l > len(msg) || i >= len(n) || !sameLabel(msg[off+1:off+1+l], n[i]) {
				return false
			}
			i++
			off += 1 + l
		}
	}
	return false
}

func sameLabel(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		x, y := b[i], s[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// A record as it lies in a message: its name and data are offsets, as
// either may point elsewhere in the message.
type record struct {
	name         int
	rtype, class uint16
	ttl          uint32
	data, end    int // msg[data:end]
}

// readRecord reads the record at off, and returns the offset past it.
func readRecord(msg []byte, off int) (r record, end int, err error) {
	e, err := skipName(msg, off)
	if err != nil || e+10 > len(msg) {
		return r, 0, errBad
	}
	r = record{name: off, rtype: binary.BigEndian.Uint16(msg[e:]), class: binary.BigEndian.Uint16(msg[e+2:]),
		ttl: binary.BigEndian.Uint32(msg[e+4:]), data: e + 10}
	r.end = r.data + int(binary.BigEndian.Uint16(msg[e+8:]))
	if r.end > len(msg) {
		return r, 0, errBad
	}
	return r, r.end, nil
}

// builder writes a message, compressing names: a name, or the end of one,
// already written is a pointer back to it.
type builder struct {
	b     []byte
	names map[string]int
}

func newBuilder(id, flags uint16) *builder {
	w := &builder{names: map[string]int{}}
	w.b = binary.BigEndian.AppendUint16(w.b, id)
	w.b = binary.BigEndian.AppendUint16(w.b, flags)
	w.b = append(w.b, make([]byte, 8)...)
	return w
}

// count sets one of the four section counts: 0 questions, 1 answers,
// 2 authority, 3 additional.
func (w *builder) count(section int, n int) {
	binary.BigEndian.PutUint16(w.b[4+2*section:], uint16(n))
}

func (w *builder) name(n name) {
	for i := range n {
		if w.names != nil { // nil: written out in full
			key := strings.ToLower(strings.Join(n[i:], "\x00"))
			if off, ok := w.names[key]; ok {
				w.b = binary.BigEndian.AppendUint16(w.b, 0xC000|uint16(off))
				return
			}
			if len(w.b) < 0x3FFF {
				w.names[key] = len(w.b)
			}
		}
		w.b = append(w.b, byte(len(n[i])))
		w.b = append(w.b, n[i]...)
	}
	w.b = append(w.b, 0)
}

// plain writes a name out in full, where compression isn't allowed.
func (w *builder) plain(n name) {
	for _, l := range n {
		w.b = append(w.b, byte(len(l)))
		w.b = append(w.b, l...)
	}
	w.b = append(w.b, 0)
}

// record writes a record's header, then data (which writes the record's
// data), then fills in the data's length.
func (w *builder) record(n name, rtype, class uint16, ttl uint32, data func()) {
	w.name(n)
	w.b = binary.BigEndian.AppendUint16(w.b, rtype)
	w.b = binary.BigEndian.AppendUint16(w.b, class)
	w.b = binary.BigEndian.AppendUint32(w.b, ttl)
	at := len(w.b)
	w.b = append(w.b, 0, 0)
	data()
	binary.BigEndian.PutUint16(w.b[at:], uint16(len(w.b)-at-2))
}

// label cuts s to the 63 bytes a label can hold.
func label(s string) string { return cut(s, 63) }

// cut cuts s to at most n bytes, at a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // inside a UTF-8 sequence
		n--
	}
	return s[:n]
}
