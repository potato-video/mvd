//go:build !files

package main

import (
	"fmt"
	"os"
)

func cmdUpload(args []string) int {
	fmt.Fprintln(os.Stderr, "mvd: this mvd was built without uploads (build tag files)")
	return 2
}
