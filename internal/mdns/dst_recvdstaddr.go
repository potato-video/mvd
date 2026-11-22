//go:build dragonfly || freebsd || netbsd || openbsd

package mdns

import (
	"net"
	"syscall"
)

// wantDestination asks for each packet's destination address, to tell a
// query sent to this machine alone from one sent to the group.
func wantDestination(fd uintptr) {
	syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_RECVDSTADDR, 1)
}

// destination reads it from a packet's control messages; nil if it isn't
// there.
func destination(oob []byte) net.IP {
	if d := cmsg(oob, syscall.IPPROTO_IP, syscall.IP_RECVDSTADDR, 4); d != nil {
		return net.IP(d)
	}
	return nil
}
