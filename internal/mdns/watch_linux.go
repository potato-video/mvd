//go:build linux

package mdns

import (
	"encoding/binary"
	"os"
	"syscall"
)

// The netlink groups for interfaces and for IPv4 and IPv6 addresses coming
// and going (RTMGRP_LINK, RTMGRP_IPV4_IFADDR, RTMGRP_IPV6_IFADDR), which
// the syscall package doesn't name.
const watchGroups = 0x1 | 0x10 | 0x100

// openWatch opens a netlink socket the kernel reports those on. It's
// non-blocking, so the Go runtime waits on it, and closing it ends a Read.
func openWatch() (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: watchGroups}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "netlink"), nil
}

// isChange says whether any message in a packet from the socket is about
// an address or an interface coming or going. Each starts with its length
// (32 bits) and type (16), in the machine's byte order. A "new link"
// message also comes for things that aren't a change at all (a Wi-Fi
// interface's scan results, its statistics): it counts only if it says
// some of the interface's flags changed (the last 32 bits of the
// ifinfomsg that follows the 16-byte header).
func isChange(b []byte) bool {
	for len(b) >= syscall.NLMSG_HDRLEN {
		l := int(binary.NativeEndian.Uint32(b))
		if l < syscall.NLMSG_HDRLEN || l > len(b) {
			return false
		}
		switch binary.NativeEndian.Uint16(b[4:]) {
		case syscall.RTM_NEWADDR, syscall.RTM_DELADDR, syscall.RTM_DELLINK:
			return true
		case syscall.RTM_NEWLINK:
			if l >= syscall.NLMSG_HDRLEN+syscall.SizeofIfInfomsg && binary.NativeEndian.Uint32(b[syscall.NLMSG_HDRLEN+12:]) != 0 {
				return true
			}
		}
		b = b[min((l+3)&^3, len(b)):] // messages are padded to four bytes
	}
	return false
}
