//go:build unix

package mdns

import (
	"net"
	"syscall"
)

// shareControl lets the socket share port 5353 with the system's responder
// (umdns or avahi on Linux, mDNSResponder on macOS): every socket on it has
// to ask to share.
func shareControl(network, address string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if serr == nil && soReusePort != 0 {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

// setup sets the socket up, once. The TTL is what RFC 6762 asks for: 255,
// so a receiver can tell a packet hasn't come from beyond the network.
// Not being told where packets were addressed isn't fatal: they're all
// taken to be the group's.
func setup(fd uintptr) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 255); err != nil {
		return err
	}
	wantDestination(fd)
	return nil
}

func join(fd uintptr, ifaddr net.IP) error {
	m := &syscall.IPMreq{Multiaddr: [4]byte{224, 0, 0, 251}}
	copy(m.Interface[:], ifaddr.To4())
	// Joined already (the interface went and came back, keeping its
	// membership): fine.
	if err := syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, m); err != nil && err != syscall.EADDRINUSE {
		return err
	}
	return nil
}

func multicastInterface(fd uintptr, ifaddr net.IP) error {
	var a [4]byte
	copy(a[:], ifaddr.To4())
	return syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, a)
}

// The same again for the IPv6 socket, where an interface is named by its
// index.
func setup6(fd uintptr) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_HOPS, 255); err != nil {
		return err
	}
	wantDestination6(fd)
	return nil
}

func join6(fd uintptr, ifindex int) error {
	m := &syscall.IPv6Mreq{Multiaddr: [16]byte{0: 0xff, 1: 0x02, 15: 0xfb}, Interface: uint32(ifindex)}
	if err := syscall.SetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_JOIN_GROUP, m); err != nil && err != syscall.EADDRINUSE {
		return err
	}
	return nil
}

func multicastInterface6(fd uintptr, ifindex int) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_IF, ifindex)
}
