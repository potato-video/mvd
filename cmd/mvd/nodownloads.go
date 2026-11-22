//go:build !downloads

package main

import (
	"fmt"
	"os"
)

func setupTransmission(string) int { return cmdTorrent(nil) }

func cmdTorrent(args []string) int {
	fmt.Fprintln(os.Stderr, "mvd: this mvd was built without downloads (build tag downloads)")
	return 2
}
