//go:build !linux

package library

import (
	"os"
	"time"
)

func changedAt(fi os.FileInfo) time.Time { return fi.ModTime() }
