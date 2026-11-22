//go:build linux

package mdns

import (
	"encoding/binary"
	"syscall"
	"testing"
)

// netlinkMsg is a message as the kernel sends one: a 16-byte header
// (length, type, and three fields not looked at), then body.
func netlinkMsg(mtype uint16, body []byte) []byte {
	b := make([]byte, 16, 16+len(body)+3)
	binary.NativeEndian.PutUint32(b, uint32(16+len(body)))
	binary.NativeEndian.PutUint16(b[4:], mtype)
	b = append(b, body...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func TestNetlinkChanges(t *testing.T) {
	link := func(change uint32) []byte { // an ifinfomsg: family, type, index, flags, change
		b := make([]byte, 16)
		binary.NativeEndian.PutUint32(b[12:], change)
		return b
	}
	for _, c := range []struct {
		name string
		msg  []byte
		want bool
	}{
		{"an address added", netlinkMsg(syscall.RTM_NEWADDR, make([]byte, 9)), true},
		{"an address removed", netlinkMsg(syscall.RTM_DELADDR, make([]byte, 8)), true},
		{"an interface up", netlinkMsg(syscall.RTM_NEWLINK, link(syscall.IFF_UP)), true},
		{"an interface gone", netlinkMsg(syscall.RTM_DELLINK, link(0)), true},
		{"Wi-Fi chatter", netlinkMsg(syscall.RTM_NEWLINK, link(0)), false},
		{"a route", netlinkMsg(syscall.RTM_NEWROUTE, make([]byte, 12)), false},
		{"chatter, then an address", append(netlinkMsg(syscall.RTM_NEWLINK, link(0)), netlinkMsg(syscall.RTM_NEWADDR, make([]byte, 9))...), true},
		{"nothing", nil, false},
		{"cut short", netlinkMsg(syscall.RTM_NEWADDR, make([]byte, 8))[:10], false},
		{"a length that lies", append(netlinkMsg(syscall.RTM_NEWROUTE, nil)[:16:16], 1, 2, 3)[:15], false},
	} {
		if got := isChange(c.msg); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// The socket opens, and closing it ends the watcher.
func TestWatchOpensAndCloses(t *testing.T) {
	events, done := changes()
	if events == nil {
		t.Skip("no netlink here")
	}
	done()
	for range events { // until it's closed
	}
}
