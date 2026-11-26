package mdns

import (
	"context"
	"net"
	"runtime"
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

	mu      sync.Mutex // everything below, the sockets, and sends: each sets the interface, then writes
	eng     *engine
	joined  map[membership]bool // interfaces joined to the group
	timer   *time.Timer         // goes off at eng.next(), or when the sockets are due to be opened again
	stopped bool
	// A send to the group failed (failed), and the sockets are to be
	// opened afresh: now, or a minute after they last were (opened).
	// failing: it's been said, and isn't again until a send works.
	failed, failing bool
	opened          time.Time
	checked         time.Time // when the sockets were last tried (check)

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
	r := &Responder{allow: allow, logf: logf, joined: map[membership]bool{}, stop: make(chan struct{}), checked: time.Now()}
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
	r.mu.Lock()
	r.serveAll()
	r.mu.Unlock()
	r.done.Add(1)
	go r.watch()
	return r, nil
}

// serveAll starts listening on each socket. Call it with r.mu held.
func (r *Responder) serveAll() {
	for _, c := range [...]*net.UDPConn{r.conn4, r.conn6} {
		if c != nil {
			r.done.Add(1)
			go r.serve(c, c == r.conn6)
		}
	}
}

// reopen opens the sockets afresh, as a send to the group failed: on a
// Mac, local network privacy can leave a socket refusing every send, and
// deaf, after its rules change (installing an app does that), while a new
// one is let through. Every network is joined, asked for and announced on
// again, which tries the new sockets. A socket that can't be opened again
// stays closed, so sends on it fail and it's tried again a minute later.
// Call it with r.mu held.
func (r *Responder) reopen(now time.Time) {
	r.failed, r.opened = false, now
	opened := false
	for _, s := range [...]struct {
		c                *net.UDPConn
		network, address string
		setup            func(fd uintptr) error
	}{{r.conn4, "udp4", "0.0.0.0:5353", setup}, {r.conn6, "udp6", "[::]:5353", setup6}} {
		if s.c == nil {
			continue
		}
		s.c.Close() // first, so the port isn't shared with it
		c, err := listen(s.network, s.address, s.setup)
		if err != nil {
			r.logf("mvd: Bonjour: %v", err)
			continue
		}
		if s.c == r.conn4 {
			r.conn4 = c
		} else {
			r.conn6 = c
		}
		opened = true
	}
	if !opened {
		return
	}
	clear(r.joined)
	r.eng.setNetworks(now, nil)
	r.serveAll()
	r.done.Add(1)
	go func() {
		defer r.done.Done()
		r.refresh()
	}()
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
	r.mu.Lock()
	v4, v6 := r.conn4 != nil, r.conn6 != nil
	r.mu.Unlock()
	nets := list(interfaces(), r.allow, v4, v6)
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
	if checkEvery > 0 && !now.Before(r.checked.Add(checkEvery)) {
		r.check(now)
	}
	r.eng.tick(now)
	r.arm(now)
}

// arm sets the timer for when the engine next wants a tick, the sockets
// are due to be tried, or opened again (opening them if that's now).
// Call it with r.mu held, after anything that may have changed that.
func (r *Responder) arm(now time.Time) {
	if r.failed && !now.Before(r.opened.Add(time.Minute)) {
		r.reopen(now)
	}
	next := r.eng.next()
	sooner := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if r.failed {
		sooner(r.opened.Add(time.Minute))
	}
	if checkEvery > 0 {
		sooner(r.checked.Add(checkEvery))
	}
	if next.IsZero() {
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
			r.mu.Lock()
			replaced := conn != r.conn4 && conn != r.conn6
			r.mu.Unlock()
			if replaced {
				return
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
	var err error
	switch {
	case n.v6 && to != nil:
		r.conn6.WriteToUDP(b, to)
		return
	case n.v6:
		if err = control(r.conn6, func(fd uintptr) error { return multicastInterface6(fd, n.ifindex) }); err == nil {
			_, err = r.conn6.WriteToUDP(b, group6)
		}
	case to != nil:
		r.conn4.WriteToUDP(b, to)
		return
	default:
		if err = control(r.conn4, func(fd uintptr) error { return multicastInterface(fd, n.ip) }); err == nil {
			_, err = r.conn4.WriteToUDP(b, group)
		}
	}
	// One to the group that fails, where a socket can stop working for
	// good: the sockets are opened again (arm).
	if checkEvery == 0 {
		return
	}
	if err != nil && !r.failing {
		r.logf("mvd: Bonjour: %v: listening afresh", err)
	}
	r.failed = r.failed || err != nil
	r.failing = err != nil
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

// checkEvery is how often the sockets are tried, where a socket can stop
// working for good: on a Mac (local network privacy). Zero: they aren't,
// and a send that fails isn't a reason to open them again either.
var checkEvery = func() time.Duration {
	if runtime.GOOS == "darwin" {
		return time.Minute
	}
	return 0
}()

// check tries the sockets: a response with nothing in it, to each group
// on one of its networks, which every responder ignores. Sending fails if
// the socket's refused (send then says so, and arm opens them again).
// Call it with r.mu held.
func (r *Responder) check(now time.Time) {
	r.checked = now
	four, six := false, false
	for _, n := range r.eng.nets {
		if n.v6 && !six || !n.v6 && !four {
			r.send(n, nil, newBuilder(0, 0x8400).b)
			six, four = six || n.v6, four || !n.v6
		}
	}
}
