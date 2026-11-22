//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package mdns

import (
	"errors"
	"syscall"
)

// changes is the system saying the machine's networks may have changed: a
// value each time an address or an interface comes or goes. Nil if it
// can't be asked. Call end when done.
func changes() (events <-chan struct{}, end func()) {
	f, err := openWatch()
	if err != nil {
		return nil, func() {}
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			switch {
			case errors.Is(err, syscall.ENOBUFS):
				// More happened than the socket could hold: something changed.
			case err != nil:
				return // closed, or broken: follow falls back to looking every minute
			case !isChange(buf[:n]):
				continue
			}
			select {
			case ch <- struct{}{}:
			default: // one's waiting to be read already
			}
		}
	}()
	return ch, func() { f.Close() }
}
