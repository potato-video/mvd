package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"mvd/internal/library"
)

// Up Next: one list of what to watch, for the web UI's home page and
// /api/up-next. Everything about what goes in it and in what order is here,
// to tune without touching anything else.
//
// What goes in, at most once per movie or show:
//   - "continue": a movie or episode you're part-way through;
//   - "next": for a show you've started, the next episode to watch;
//   - "new": a movie added in the last newFor (30 days) and not watched, or
//     a show with new episodes, as the one you'd watch next.
//
// The order is by the latest thing that happened to each: when you last
// played it, or when it arrived; part-watched things get a head start of
// continueBoost, so a big delivery of new things doesn't bury what you're
// in the middle of.

const (
	upNextLimit   = 40             // the list stops here
	continueBoost = 12 * time.Hour // a part-watched thing ranks as if played this much later
)

// rank is what an item sorts by: newest first.
func rank(reason string, when time.Time) time.Time {
	if reason == "continue" {
		return when.Add(continueBoost)
	}
	return when
}

// upNextItem is one entry: a movie or an episode (as the API shows them
// everywhere), why it's here, and for a show how many new episodes wait.
type upNextItem struct {
	Reason   string // "continue", "next" or "new"
	NewCount int    // new episodes of the show, not yet watched, this one included
	View     any    // movieView or episodeView
	ShowID   string // the episode's show
	rank     time.Time
}

// MarshalJSON is the movie or episode, plus "reason", "new_count" and the
// pictures to show it with (upNextArt).
func (u upNextItem) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(u.View)
	if err != nil {
		return nil, err
	}
	extra, _ := json.Marshal(struct {
		Reason   string `json:"reason"`
		NewCount int    `json:"new_count,omitempty"`
		upNextArt
	}{u.Reason, u.NewCount, upNextPictures(u.View)})
	b = bytes.TrimSuffix(bytes.TrimSpace(b), []byte("}"))
	return append(append(b, ','), extra[1:]...), nil
}

// upNextArt is the pictures to show an entry with, for the API and the
// website alike. Only pictures that exist are listed, so "" is none at all.
type upNextArt struct {
	// The feature's backdrop names the entry: a movie's fanart, else its
	// poster; an episode's show's fanart, else its still.
	Backdrop string `json:"backdrop,omitempty"`
	// A row's 16:9 thumb picks out the episode: its still, else the show's
	// fanart. A movie's is its backdrop.
	RowThumb string `json:"row_thumb,omitempty"`
	// The Top Shelf's 2:3 picture: a movie's poster, an episode's season's
	// (else show's) poster; without one, the backdrop, cropped.
	Shelf string `json:"shelf_image,omitempty"`
}

func upNextPictures(view any) upNextArt {
	switch v := view.(type) {
	case movieView:
		b := or(v.Fanart, v.Poster)
		return upNextArt{Backdrop: b, RowThumb: b, Shelf: or(v.Poster, b)}
	case episodeView:
		b := or(v.Fanart, v.Thumb)
		return upNextArt{Backdrop: b, RowThumb: or(v.Thumb, v.Fanart), Shelf: or(v.Poster, b)}
	}
	return upNextArt{}
}

// or is a, else b.
func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// upNext builds the list.
func (s shelf) upNext() []upNextItem {
	all := s.state().All()
	cutoff := s.sv.clock().Add(-newFor)
	var items []upNextItem

	for _, m := range s.Movies() {
		p := all[m.ID]
		switch {
		case p.Hidden:
		case p.Position > 0:
			items = append(items, upNextItem{Reason: "continue", View: s.movieView(m, true),
				rank: rank("continue", p.Updated)})
		case !p.Watched && m.Added.After(cutoff):
			items = append(items, upNextItem{Reason: "new", View: s.movieView(m, true),
				rank: rank("new", m.Added)})
		}
	}

	for _, sh := range s.Shows() {
		// New episodes: how many, and when the latest came.
		n, newest := 0, time.Time{}
		for _, e := range sh.InOrder() {
			if e.Added.After(cutoff) && !all[e.ID].Watched {
				n++
				if e.Added.After(newest) {
					newest = e.Added
				}
			}
		}

		pick, partway, started, last := showNext(sh, all)
		reason, when := "", time.Time{}
		switch {
		case pick == nil:
		case partway:
			reason, when = "continue", all[pick.ID].Updated
		case started:
			reason, when = "next", last
			if newest.After(when) {
				when = newest // new episodes of a show you watch bring it up
			}
		case n > 0:
			reason, when = "new", newest
		default:
			pick = nil // never started, nothing new: not for Up Next
		}
		if pick == nil {
			continue
		}
		// Taken out of Up Next: back once you play it again or more arrives.
		if h := all[sh.ID]; h.Hidden && !when.After(h.Updated) {
			continue
		}
		items = append(items, upNextItem{Reason: reason, NewCount: n, View: s.episodeView(pick, true),
			ShowID: sh.ID, rank: rank(reason, when)})
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].rank.After(items[j].rank) })
	if len(items) > upNextLimit {
		items = items[:upNextLimit]
	}
	return items
}

// showNext is a show's next episode, the same everywhere: its Up Next
// entry, its page on the website and in the Apple app (next_episode_id).
// The one you're part-way through (the one played last), else the one
// after the episode watched most recently (nextToWatch). Never a special:
// they're outside the run of the show, so watching one doesn't move it on.
// started and last are nextToWatch's.
func showNext(sh *library.Show, all map[string]Progress) (next *library.Episode, partway, started bool, last time.Time) {
	var when time.Time
	for _, e := range sh.InOrder() {
		if p := all[e.ID]; p.Position > 0 && (next == nil || p.Updated.After(when)) {
			next, when = e, p.Updated
		}
	}
	n, started, last := nextToWatch(sh, all)
	if next != nil {
		return next, true, started, last
	}
	return n, false, started, last
}

// removeFromUpNext takes something out of Up Next, leaving progress as it
// is: a movie until you play it again; a show (by way of any of its
// episodes) until you play any of it again or more episodes arrive. False
// for an id that isn't in the library.
func (s shelf) removeFromUpNext(id string) bool {
	switch {
	case s.Movie(id) != nil, s.Show(id) != nil:
		s.state().Hide(id)
	case s.Episode(id) != nil:
		s.state().Hide(s.Episode(id).Show.ID)
	default:
		return false
	}
	return true
}

func (sv *Server) apiUpNext(w http.ResponseWriter, r *http.Request) {
	items := sv.shelf(r).upNext()
	if items == nil {
		items = []upNextItem{}
	}
	writeJSON(w, items)
}

func (sv *Server) apiRemoveFromUpNext(w http.ResponseWriter, r *http.Request) {
	if !sv.shelf(r).removeFromUpNext(r.PathValue("id")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
