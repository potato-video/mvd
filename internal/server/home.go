package server

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"mvd/internal/config"
	"mvd/internal/library"
)

// The pieces of a home page besides Up Next (upnext.go): new episodes
// and movies, which the API also offers on their own (/api/episodes/new,
// /api/movies?since=...).

// newFor is how long something counts as new: the default for
// /api/episodes/new and the web UI's home page.
const newFor = 30 * 24 * time.Hour

type filter struct {
	since     time.Duration // added within this long; 0 for any time
	unwatched bool          // only things not yet watched
	newest    bool          // newest first (implied by since), rather than by title
}

// listFilter reads ?since=30d and ?unwatched=true.
func listFilter(r *http.Request) (filter, error) {
	var f filter
	q := r.URL.Query()
	if v := q.Get("since"); v != "" {
		d, err := config.ParseAge(v)
		if err != nil {
			return f, fmt.Errorf("since: %v", err)
		}
		f.since, f.newest = d, d > 0
	}
	switch v := q.Get("unwatched"); v {
	case "", "false", "0":
	case "true", "1":
		f.unwatched = true
	default:
		return f, fmt.Errorf("unwatched: %q isn't true or false", v)
	}
	return f, nil
}

// movieList is the movies, by title, or newest first when filtered by age.
func (s shelf) movieList(f filter) []movieView {
	cutoff := s.sv.clock().Add(-f.since)
	var ms []*library.Movie
	for _, m := range s.Movies() {
		if f.since > 0 && m.Added.Before(cutoff) {
			continue
		}
		if f.unwatched && s.state().Get(m.ID).Watched {
			continue
		}
		ms = append(ms, m)
	}
	if f.newest {
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].Added.After(ms[j].Added) })
	}
	out := make([]movieView, 0, len(ms))
	for _, m := range ms {
		out = append(out, s.movieView(m, false))
	}
	return out
}

// nextToWatch is a show's next episode: the one after the episode you
// watched most recently (so a rewatch of episode 1 leads to episode 2,
// whatever you'd watched before), or the first unwatched one if you've
// watched none. Episodes marked watched together (a season at once) count
// as one go, ending at the furthest. Nil if the last one watched is the
// last there is. started says whether you've watched any; when is when you
// last did.
func nextToWatch(sh *library.Show, all map[string]Progress) (next *library.Episode, started bool, when time.Time) {
	eps := sh.InOrder()
	for _, e := range eps {
		if p := all[e.ID]; p.Watched && p.Updated.After(when) {
			when, started = p.Updated, true
		}
	}
	if !started {
		for _, e := range eps {
			if !all[e.ID].Watched {
				return e, false, when
			}
		}
		return nil, false, when
	}
	last := -1
	for i, e := range eps {
		if p := all[e.ID]; p.Watched && when.Sub(p.Updated) < batch {
			last = i
		}
	}
	if last+1 < len(eps) {
		return eps[last+1], true, when
	}
	return nil, true, when
}

// batch: episodes marked watched within this of each other were marked
// together, not watched one after another.
const batch = time.Minute

// newEpisodeView is a show with new episodes, as the one to watch next.
type newEpisodeView struct {
	episodeView
	NewCount int `json:"new_count"` // new episodes not yet watched, this one included
}

// newEpisodes is each show with new episodes you haven't watched, as the
// episode to watch next: not the first new one, but the first unwatched one
// (so a season shows as episode 1 until that's watched, then 2, and a new
// E02 waits behind an unwatched E01 however old that is). Shows with the
// newest arrivals first. since 0 means any time. Specials aren't counted.
func (s shelf) newEpisodes(since time.Duration) []newEpisodeView {
	all := s.state().All()
	cutoff := s.sv.clock().Add(-since)
	type stamped struct {
		v newEpisodeView
		t time.Time
	}
	var items []stamped
	for _, sh := range s.Shows() {
		var newest time.Time
		n := 0
		for _, e := range sh.InOrder() {
			if (since > 0 && e.Added.Before(cutoff)) || all[e.ID].Watched {
				continue
			}
			n++
			if e.Added.After(newest) {
				newest = e.Added
			}
		}
		if n == 0 {
			continue
		}
		if next, _, _ := nextToWatch(sh, all); next != nil {
			items = append(items, stamped{newEpisodeView{s.episodeView(next, false), n}, newest})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].t.After(items[j].t) })
	out := make([]newEpisodeView, 0, len(items))
	for _, it := range items {
		out = append(out, it.v)
	}
	return out
}

// apiNewEpisodes: ?since=30d by default; ?since=0 for any time.
func (sv *Server) apiNewEpisodes(w http.ResponseWriter, r *http.Request) {
	since := newFor
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := config.ParseAge(v)
		if err != nil {
			http.Error(w, "since: "+err.Error(), http.StatusBadRequest)
			return
		}
		since = d
	}
	writeJSON(w, sv.shelf(r).newEpisodes(since))
}
