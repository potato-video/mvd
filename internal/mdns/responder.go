package mdns

import (
	"context"
	"net"
	"slices"
	"sync"
	"time"
)

var (
	group  = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	group6 = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}
)

// Responder answers mDNS queries for a service, and announces it. It's
// the engine's plumbing: the sockets (one for IPv4, one for IPv6), the
// networks, and a timer for when the engine next has something to send.
type Responder struct {
	conn4, conn6 *net.UDPConn // nil: the machine hasn't that family, or the port couldn't be had
	allow        func(ifname string, ip net.IP) bool
	logf         func(format string, args ...any)

	mu      sync.Mutex // everything below, and sends: each sets the interface, then writes
	eng     *engine
	joined  map[membership]bool // interfaces joined to the group
	timer   *time.Timer         // goes off at eng.next()
	stopped bool

	stop chan struct{}
	done sync.WaitGroup
}

type membership struct {
	ifindex int
	v6      bool
}

// Start listens on the mDNS port, beside the system's responder if it has
// one, and announces the service on each network allow accepts (nil: every
// private network), over IPv4 and IPv6, whichever the machine has. It
// watches for networks coming and going. logf says what it's doing.
func Start(svc Service, allow func(ifname string, ip net.IP) bool, logf func(string, ...any)) (*Responder, error) {
	r := &Responder{allow: allow, logf: logf, joined: map[membership]bool{}, stop: make(chan struct{})}
	var err, err6 error
	if r.conn4, err = listen("udp4", "0.0.0.0:5353", setup); err != nil {
		if r.conn6, err6 = listen("udp6", "[::]:5353", setup6); err6 != nil {
			return nil, err
		}
	} else {
		r.conn6, _ = listen("udp6", "[::]:5353", setup6) // no IPv6 here: IPv4 alone
	}
	r.eng = newEngine(svc, r.send, logf)
	r.timer = time.AfterFunc(time.Hour, r.wake)
	r.refresh()
	for _, c := range [...]*net.UDPConn{r.conn4, r.conn6} {
		if c != nil {
			r.done.Add(1)
			go r.serve(c, c == r.conn6)
		}
	}
	r.done.Add(1)
	go r.watch()
	return r, nil
}

// listen opens a socket on the mDNS port that shares it, set up by setup.
func listen(network, address string, setup func(fd uintptr) error) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: shareControl}
	pc, err := lc.ListenPacket(context.Background(), network, address)
	if err != nil {
		return nil, err
	}
	c := pc.(*net.UDPConn)
	if err := control(c, setup); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Close says goodbye on every network, so browsers drop the service now
// rather than when it times out, and stops.
func (r *Responder) Close() {
	close(r.stop)
	r.mu.Lock()
	r.stopped = true
	r.timer.Stop()
	r.eng.goodbye(time.Now())
	r.mu.Unlock()
	for _, c := range [...]*net.UDPConn{r.conn4, r.conn6} {
		if c != nil {
			c.Close()
		}
	}
	r.done.Wait()
}

// refresh finds the networks now, joins the group on new interfaces, and
// tells the engine. Call it with r.mu not held.
func (r *Responder) refresh() {
	nets := list(interfaces(), r.allow, r.conn4 != nil, r.conn6 != nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	// An interface that's gone is forgotten, so that it's joined afresh if
	// it comes back: the system drops the membership with the interface.
	for m := range r.joined {
		if !slices.ContainsFunc(nets, func(n *network) bool { return n.ifindex == m.ifindex && n.v6 == m.v6 }) {
			delete(r.joined, m)
		}
	}
	var ok []*network
	for _, n := range nets {
		if m := (membership{n.ifindex, n.v6}); !r.joined[m] {
			var err error
			if n.v6 {
				err = control(r.conn6, func(fd uintptr) error { return join6(fd, n.ifindex) })
			} else {
				err = control(r.conn4, func(fd uintptr) error { return join(fd, n.ip) })
			}
			if err != nil {
				r.logf("mvd: Bonjour: can't listen on %s: %v", n.name, err)
				continue
			}
			r.joined[m] = true
		}
		ok = append(ok, n)
	}
	now := time.Now()
	r.eng.setNetworks(now, ok)
	r.eng.tick(now)
	r.arm(now)
}

// wake is the timer going off: the engine has something due.
func (r *Responder) wake() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	now := time.Now()
	r.eng.tick(now)
	r.arm(now)
}

// arm sets the timer for when the engine next wants a tick. Call it with
// r.mu held, after anything that may have changed that.
func (r *Responder) arm(now time.Time) {
	if next := r.eng.next(); next.IsZero() {
		r.timer.Stop()
	} else {
		r.timer.Reset(max(next.Sub(now), 0))
	}
}

// watch keeps the networks up to date: a second after the system says
// something changed, or every minute where it doesn't say.
func (r *Responder) watch() {
	defer r.done.Done()
	events, done := changes()
	defer done()
	follow(events, time.Second, time.Minute, r.stop, r.refresh)
}

func (r *Responder) serve(conn *net.UDPConn, v6 bool) {
	defer r.done.Done()
	buf := make([]byte, 9000)
	oob := make([]byte, 128)
	failing := false
	for {
		n, oobn, _, from, err := conn.ReadMsgUDP(buf, oob)
		if err != nil {
			select {
			case <-r.stop:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			// Something passing (an interface going down, say): say so
			// once, and keep listening, rather than end Bonjour until the
			// server restarts.
			if !failing {
				r.logf("mvd: Bonjour: %v", err)
			}
			failing = true
			select {
			case <-r.stop:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		failing = false
		// Addressed to this machine alone, not the group? Where the
		// system doesn't say, it's taken to be the group. Over IPv6 it
		// says which interface the packet came in on, too.
		var dst net.IP
		ifindex := 0
		if v6 {
			dst, ifindex = destination6(oob[:oobn])
		} else {
			dst = destination(oob[:oobn])
		}
		direct := dst != nil && !dst.IsMulticast()
		r.mu.Lock()
		if !r.stopped {
			nw, ok := networkFor(r.eng.nets, from.IP)
			if v6 {
				nw, ok = networkFor6(r.eng.nets, from, ifindex)
			}
			if ok {
				now := time.Now()
				r.eng.packet(now, nw, from, direct, buf[:n])
				r.arm(now)
			}
		}
		r.mu.Unlock()
	}
}

// send sends b on n: to one address, or (to nil) to the group, out of n's
// interface. Call it with r.mu held.
func (r *Responder) send(n *network, to *net.UDPAddr, b []byte) {
	switch {
	case n.v6 && to != nil:
		r.conn6.WriteToUDP(b, to)
	case n.v6:
		if control(r.conn6, func(fd uintptr) error { return multicastInterface6(fd, n.ifindex) }) == nil {
			r.conn6.WriteToUDP(b, group6)
		}
	case to != nil:
		r.conn4.WriteToUDP(b, to)
	default:
		if control(r.conn4, func(fd uintptr) error { return multicastInterface(fd, n.ip) }) == nil {
			r.conn4.WriteToUDP(b, group)
		}
	}
}

func control(c *net.UDPConn, f func(fd uintptr) error) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = f(fd) }); err != nil {
		return err
	}
	return ferr
}
