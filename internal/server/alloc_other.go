//go:build !linux

package server

import "os"

// allocate gives f its full size up front ([server] upload_preallocate).
func allocate(f *os.File, size int64) error { return f.Truncate(size) }
