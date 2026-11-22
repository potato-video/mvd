package server

import (
	"os"
	"syscall"
)

// allocate gives f its full size on the drive up front ([server]
// upload_preallocate): reserved, where the filesystem can (ext4, XFS), else
// by setting its length (exFAT then writes it all in zeros).
func allocate(f *os.File, size int64) error {
	if syscall.Fallocate(int(f.Fd()), 0, 0, size) == nil {
		return nil
	}
	return f.Truncate(size)
}
