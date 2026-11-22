package server

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReportWatchedRule(t *testing.T) {
	cases := []struct {
		pos, dur float64
		watched  bool
	}{
		{1, 120, false},      // 1 s into a 2-minute clip: "the last 3 minutes" is all of it
		{110, 120, true},     // 90% of the clip
		{-5, 100, false},     // nonsense from a player
		{600, 10500, false},  // 10 minutes into a movie
		{10400, 10500, true}, // in the credits
		{9500, 10500, true},  // past 90%
		{1250, 1320, true},   // a 22-minute episode, 70 s from the end
		{900, 1320, false},   // 15 minutes in
	}
	for _, c := range cases {
		st := &State{m: map[string]Progress{}}
		if p := st.Report("x", c.pos, c.dur); p.Watched != c.watched {
			t.Errorf("pos %v of %v: watched=%v, want %v", c.pos, c.dur, p.Watched, c.watched)
		}
	}
	st := &State{m: map[string]Progress{}}
	if p := st.Report("x", 500, 100); p.Position > 100 {
		t.Errorf("position past the end kept: %+v", p)
	}
}

// Flushes from the ticker and from shutdown can overlap; neither may leave
// a half-written file, and a failed write must be retried later.
func TestFlushConcurrentAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, _ := loadState(path)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st.SetWatched(string(rune('a'+i)), true)
			if err := st.Flush(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	back, err := loadState(path)
	if err != nil || len(back.All()) != 20 {
		t.Fatalf("after concurrent flushes: %d entries, %v", len(back.All()), err)
	}

	bad := &State{path: filepath.Join(path, "not-a-dir", "state.json"), m: map[string]Progress{}}
	bad.SetWatched("x", true)
	if bad.Flush() == nil {
		t.Fatal("flush into a file-as-folder should fail")
	}
	bad.path = filepath.Join(t.TempDir(), "state.json")
	if err := bad.Flush(); err != nil {
		t.Fatal(err)
	}
	if back, _ := loadState(bad.path); len(back.All()) != 1 {
		t.Errorf("a failed flush lost its changes: the retry wrote nothing")
	}
}

// A glance isn't watching: until resume_after (a minute, by default) in,
// there's no resume point and it isn't "in progress".
func TestResumeAfter(t *testing.T) {
	st := &State{m: map[string]Progress{}, resumeAfter: time.Minute}
	if p := st.Report("x", 20, 3000); p.Position != 0 {
		t.Errorf("20 s in counted: %+v", p)
	}
	if _, kept := st.All()["x"]; kept {
		t.Errorf("a glance was remembered")
	}
	if p := st.Report("x", 90, 3000); p.Position != 90 {
		t.Errorf("90 s in: %+v", p)
	}
	// A short clip (under ten minutes) still counts at 90%, whatever the limit.
	if p := st.Report("clip", 55, 60); !p.Watched {
		t.Errorf("a one-minute clip played through: %+v", p)
	}
	// Watched stays watched through a rewatch's first seconds.
	st.SetWatched("w", true)
	if p := st.Report("w", 10, 3000); !p.Watched {
		t.Errorf("rewatching cleared watched: %+v", p)
	}
}

// Played is when something was last played (or marked watched): clearing
// the position or hiding it from Up Next keeps it, unlike Updated. State
// written before Played existed falls back to Updated.
func TestPlayed(t *testing.T) {
	st := &State{m: map[string]Progress{}}
	if p := st.Report("x", 600, 3300); p.Played.IsZero() {
		t.Fatalf("report: %+v", p)
	}
	played := st.Get("x").Played
	time.Sleep(2 * time.Millisecond)
	st.Hide("x")
	st.ClearPosition("x")
	if p := st.Get("x"); !p.Played.Equal(played) {
		t.Errorf("hide and clear moved Played: %v, was %v", p.Played, played)
	}
	if p := st.SetWatched("y", true); p.Played.IsZero() {
		t.Errorf("marked watched: %+v", p)
	}
	if p := st.SetWatched("y", false); !p.Played.IsZero() {
		t.Errorf("marked unwatched: %+v", p)
	}

	old := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := (Progress{Watched: true, Updated: old}).LastPlayed(); !got.Equal(old) {
		t.Errorf("old watched entry: %v", got)
	}
	if got := (Progress{Hidden: true, Updated: old}).LastPlayed(); !got.IsZero() {
		t.Errorf("old entry only hidden: %v", got)
	}
}
