//go:build unix

package remux

import "syscall"

// pause stops ffmpeg where it is, for being far enough ahead of the player;
// resume carries it on.
func (r *run) pause() {
	if r.cmd.Process.Signal(syscall.SIGSTOP) == nil {
		r.paused = true
	}
}

func (r *run) resume() {
	r.cmd.Process.Signal(syscall.SIGCONT)
	r.paused = false
}
