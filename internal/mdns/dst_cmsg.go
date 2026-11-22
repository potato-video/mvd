//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package mdns

import (
	"encoding/binary"
	"syscall"
)

// cmsg returns the data of a packet's first control message, if it's of
// this level and type and holds size bytes; nil otherwise. Only one
// message is ever asked for on a socket, so only the first is looked at.
// Its header is a length (a C size_t on Linux, 32 bits elsewhere), a level
// and a type, in the machine's byte order. How far the data is padded from
// the header differs by system, so it's found from the length, which
// counts the header, the padding and the data.
func cmsg(oob []byte, level, typ, size int) []byte {
	const h = syscall.SizeofCmsghdr
	if len(oob) < h {
		return nil
	}
	var l uint64
	if h-8 == 8 {
		l = binary.NativeEndian.Uint64(oob)
	} else {
		l = uint64(binary.NativeEndian.Uint32(oob))
	}
	if l < uint64(h+size) || l > uint64(len(oob)) ||
		int(binary.NativeEndian.Uint32(oob[h-8:])) != level ||
		int(binary.NativeEndian.Uint32(oob[h-4:])) != typ {
		return nil
	}
	return oob[int(l)-size : l]
}
