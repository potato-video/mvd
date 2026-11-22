//go:build unix

package library

import "syscall"

// FreeSpace is how much a user can write to dir's drive.
func FreeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

// sameDrive says whether two paths are on the same drive (so a move between
// them frees nothing).
func sameDrive(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return false
	}
	return uint64(sa.Dev) == uint64(sb.Dev)
}
