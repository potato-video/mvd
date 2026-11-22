//go:build !unix

package library

// FreeSpace isn't known here: uploads and unpacking go ahead, and fail if
// the drive fills.
func FreeSpace(dir string) (int64, bool) { return 0, false }

func sameDrive(a, b string) bool { return false }
