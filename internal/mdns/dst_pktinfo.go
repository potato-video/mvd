//go:build linux || darwin

package mdns

import (
	"net"
	"syscall"
)

// wantDestination asks for each packet's destination address, to tell a
// query sent to this machine alone from one sent to the group.
func wantDestination(fd uintptr) {
	syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_PKTINFO, 1)
}

// destination reads it from a packet's control messages; nil if it isn't
// there. It's the last four bytes of an in_pktinfo: the interface's index,
// the address to reply from, and the destination.
func destination(oob []byte) net.IP {
	if d := cmsg(oob, syscall.IPPROTO_IP, syscall.IP_PKTINFO, 12); d != nil {
		return net.IP(d[8:12])
	}
	return nil
}
