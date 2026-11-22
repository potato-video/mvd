package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mvd/internal/library"
)

// Progress is what the server remembers per item: where you got to, and
// whether you've seen it.
type Progress struct {
	Position float64   `json:"position,omitempty"` // seconds
	Duration float64   `json:"duration,omitempty"`
	Watched  bool      `json:"watched,omitempty"`
	Hidden   bool      `json:"hidden,omitempty"` // taken out of Up Next (until it's played again)
	Updated  time.Time `json:"updated"`
	// Played is when it was last played or marked watched. Unlike Updated,
	// clearing the position or hiding it from Up Next leaves it alone.
	Played time.Time `json:"played,omitzero"`
}

// LastPlayed is Played, or for state saved before Played was kept,
// Updated if it was watched or started.
func (p Progress) LastPlayed() time.Time {
	if p.Played.IsZero() && (p.Watched || p.Position > 0) {
		return p.Updated
	}
	return p.Played
}

// State is the progress store: one JSON file, written in the background
// at most every few seconds rather than on every report.
type State struct {
	mu    sync.Mutex
	path  string
	m     map[string]Progress
	dirty bool

	flushMu sync.Mutex // one write at a time: they share the temp file

	// resumeAfter: a position short of this isn't a resume point, so a
	// glance at something doesn't leave it "in progress" (resume_after).
	resumeAfter time.Duration
}

func loadState(path string) (*State, error) {
	st := &State{path: path, m: map[string]Progress{}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &st.m); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return st, nil
}

func (st *State) Get(id string) Progress {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.m[id]
}

// Report records a playback position. Near enough the end counts as
// watched, and the position resets so it isn't offered as "continue".
// Watched sticks through a rewatch; only the resume point moves.
func (st *State) Report(id string, pos, dur float64) Progress {
	pos, dur = max(pos, 0), max(dur, 0)
	if dur > 0 {
		pos = min(pos, dur)
	}
	now := time.Now()
	p := Progress{Position: pos, Duration: dur, Watched: st.Get(id).Watched, Updated: now, Played: now}
	if pos < st.resumeAfter.Seconds() {
		p.Position = 0 // not far enough in to be worth resuming
	}
	if nearEnd(pos, dur) {
		p = Progress{Watched: true, Duration: dur, Updated: now, Played: now}
	}
	st.set(id, p)
	return p
}

// nearEnd: the last 10%, or (for anything long enough to have them) the
// last three minutes of credits. Without that proviso, a clip under three
// minutes counted as watched the moment it started.
func nearEnd(pos, dur float64) bool {
	if dur <= 0 || pos <= 0 {
		return false
	}
	return pos/dur >= 0.9 || (dur >= 10*60 && dur-pos < 180)
}

// ClearPosition forgets where you'd got to, keeping whether you've
// watched it.
func (st *State) ClearPosition(id string) Progress {
	p := st.Get(id)
	p.Position, p.Updated = 0, time.Now()
	st.set(id, p)
	return p
}

// Hide takes a movie or show out of Up Next, keeping where you'd got to.
// Playing a movie again replaces the marker (Report starts afresh); for a
// show, upNext compares its time with the show's latest.
func (st *State) Hide(id string) {
	p := st.Get(id)
	p.Hidden, p.Updated = true, time.Now()
	st.set(id, p)
}

func (st *State) SetWatched(id string, watched bool) Progress {
	p := Progress{Watched: watched, Updated: time.Now()}
	if watched {
		p.Played = p.Updated
	}
	st.set(id, p)
	return p
}

func (st *State) set(id string, p Progress) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !p.Watched && p.Position == 0 && !p.Hidden {
		delete(st.m, id) // nothing worth keeping
	} else {
		st.m[id] = p
	}
	st.dirty = true
}

func (st *State) All() map[string]Progress {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]Progress, len(st.m))
	for k, v := range st.m {
		out[k] = v
	}
	return out
}

// Flush writes the file if anything changed, via a temp file so a power cut
// mid-write can't lose the lot.
func (st *State) Flush() error {
	st.flushMu.Lock()
	defer st.flushMu.Unlock()
	st.mu.Lock()
	if !st.dirty {
		st.mu.Unlock()
		return nil
	}
	b, err := json.MarshalIndent(st.m, "", " ")
	st.dirty = false
	st.mu.Unlock()
	if err == nil {
		err = st.write(b)
	}
	if err != nil {
		st.mu.Lock()
		st.dirty = true // try again next time rather than lose the changes
		st.mu.Unlock()
	}
	return err
}

func (st *State) write(b []byte) error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	return library.WriteFileAtomic(st.path, b, 0o644)
}

func (st *State) flushEvery(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			st.Flush()
		case <-stop:
			return
		}
	}
}
