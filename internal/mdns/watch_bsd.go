//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package mdns

import (
	"os"
	"syscall"
)

// openWatch opens a routing socket, which the kernel reports changes to
// addresses and interfaces on (and to routes, which aren't wanted). It's
// non-blocking, so the Go runtime waits on it, and closing it ends a Read.
func openWatch() (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "route"), nil
}

// isChange says whether a message from the socket (one a read) is about
// an address or an interface: its type is its fourth byte, after a 16-bit
// length and a version.
func isChange(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch b[3] {
	case syscall.RTM_NEWADDR, syscall.RTM_DELADDR, syscall.RTM_IFINFO:
		return true
	}
	return false
}
