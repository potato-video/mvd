//go:build linux && !(mips || mipsle || mips64 || mips64le)

package mdns

// SO_REUSEPORT, which the syscall package only names on some Linux
// architectures. SO_REUSEADDR is enough to share the port with umdns or
// avahi; this is in case one sets only SO_REUSEPORT.
const soReusePort = 15
