//go:build !unix

package remux

// Without signals to stop and continue it, ffmpeg runs to the end.
func (r *run) pause()  {}
func (r *run) resume() {}
