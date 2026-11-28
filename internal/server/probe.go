package server

import (
	"log"
	"sync"
	"time"

	"mvd/internal/library"
)

// The probe pass: after a scan, ffprobe each video file the index doesn't
// have tracks for, one at a time in the background (about a second a file
// on the router), and swap in a copy of the index with them every few
// minutes and at the end. A rescan stops the pass and starts another from
// the new index.

const probeFlushEvery = 2 * time.Minute

type probing struct {
	mu      sync.Mutex
	stop    chan struct{} // closed to stop the running pass
	done    chan struct{} // closed when it has stopped
	pending library.Probed
}

// takePending returns what's been probed but isn't in the index yet, and
// forgets it.
func (sv *Server) takePending() library.Probed {
	sv.probes.mu.Lock()
	defer sv.probes.mu.Unlock()
	p := sv.probes.pending
	sv.probes.pending = nil
	return p
}

// startProbing stops the running pass, if any, and starts one for ix's
// unprobed files. Called by rescan, with scanMu held.
func (sv *Server) startProbing(ix *library.Index) {
	pr := &sv.probes
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.stop != nil {
		close(pr.stop) // not waited for: it may be waiting for scanMu
		pr.stop, pr.done = nil, nil
	}
	if library.FFprobe() == "" {
		return
	}
	todo := ix.Unprobed()
	if len(todo) == 0 {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	pr.stop, pr.done = stop, done
	go sv.probePass(todo, stop, done)
}

func (sv *Server) probePass(todo []string, stop, done chan struct{}) {
	defer close(done)
	last := time.Now()
	for _, f := range todo {
		select {
		case <-stop:
			return // what's pending goes into the next index
		default:
		}
		sv.probes.mu.Lock()
		seen := sv.probes.pending[f] != nil // an earlier pass got to it
		sv.probes.mu.Unlock()
		if seen {
			continue
		}
		mf, err := library.Probe(f)
		if err != nil {
			continue // gone since the scan
		}
		sv.probes.mu.Lock()
		if sv.probes.pending == nil {
			sv.probes.pending = library.Probed{}
		}
		sv.probes.pending[f] = mf
		sv.probes.mu.Unlock()
		if time.Since(last) >= probeFlushEvery {
			sv.applyProbes()
			last = time.Now()
		}
	}
	sv.applyProbes()
}

// applyProbes swaps in a copy of the index with what's been probed, and
// saves it.
func (sv *Server) applyProbes() {
	sv.scanMu.Lock()
	defer sv.scanMu.Unlock()
	p := sv.takePending()
	ix := sv.ix.Load()
	if len(p) == 0 || ix == nil {
		return
	}
	ix = ix.WithMedia(p)
	sv.ix.Store(ix)
	sv.saveIndex(ix)
}

// waitProbing waits for the running probe pass, if any, to finish. For tests.
func (sv *Server) waitProbing() {
	sv.probes.mu.Lock()
	done := sv.probes.done
	sv.probes.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (sv *Server) saveIndex(ix *library.Index) {
	if sv.cfg.IndexFile == "" {
		return
	}
	if err := ix.Save(sv.cfg.IndexFile); err != nil {
		log.Printf("mvd: saving index: %v", err)
	}
}

// fileView is a video file as ffprobe saw it: GET /api/item's files.
type fileView struct {
	Container string          `json:"container"`
	Duration  float64         `json:"duration,omitempty"`
	Bitrate   int             `json:"bitrate,omitempty"`
	Tracks    []library.Track `json:"tracks"`
}

// fileViews describes a movie's parts or an episode's file; nil unless every
// one of them has been probed.
func fileViews(media []*library.MediaFile, n int) []fileView {
	if len(media) != n {
		return nil
	}
	out := make([]fileView, 0, n)
	for _, mf := range media {
		if !mf.OK() {
			return nil
		}
		tracks := mf.Tracks
		if tracks == nil {
			tracks = []library.Track{}
		}
		out = append(out, fileView{Container: mf.Container, Duration: mf.Duration, Bitrate: mf.Bitrate, Tracks: tracks})
	}
	return out
}
