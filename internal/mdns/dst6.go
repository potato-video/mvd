//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package mdns

import (
	"encoding/binary"
	"net"
	"syscall"
)

// wantDestination6 asks, on the IPv6 socket, for each packet's destination
// address and the interface it came in on.
func wantDestination6(fd uintptr) {
	syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, wantPktinfo6, 1)
}

// destination6 reads them from a packet's control messages (an
// in6_pktinfo: the address, then the interface's index in the machine's
// byte order); nil and 0 if they aren't there.
func destination6(oob []byte) (dst net.IP, ifindex int) {
	if d := cmsg(oob, syscall.IPPROTO_IPV6, pktinfo6, 20); d != nil {
		return net.IP(d[:16]), int(binary.NativeEndian.Uint32(d[16:]))
	}
	return nil, 0
}
