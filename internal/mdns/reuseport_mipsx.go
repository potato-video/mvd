//go:build linux && (mips || mipsle || mips64 || mips64le)

package mdns

import "syscall"

// SO_REUSEPORT has its own number on MIPS, which syscall names. (The file
// is _mipsx, not _mips: that name would keep it to big-endian MIPS alone.)
const soReusePort = syscall.SO_REUSEPORT
