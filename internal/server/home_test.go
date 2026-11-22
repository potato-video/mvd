package server

import (
	"net/http"
	"testing"
	"time"

	"mvd/internal/library"
)

// A season arrives all at once: New episodes shows its first episode (and
// how many are waiting) until that's watched, then the next.
func TestNewEpisodesOnePerShow(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var eps []newEpisodeView
	ts.json("GET", "/api/episodes/new", "", &eps)
	if len(eps) != 1 || eps[0].Season != 1 || eps[0].Episode != 1 || eps[0].NewCount != 2 {
		t.Fatalf("new episodes: %+v", eps)
	}
	ts.do("POST", "/api/progress/"+eps[0].ID, `{"watched": true}`, nil).Body.Close()
	eps = nil
	ts.json("GET", "/api/episodes/new", "", &eps)
	if len(eps) != 1 || eps[0].Episode != 2 || eps[0].NewCount != 1 {
		t.Errorf("after watching episode 1: %+v", eps)
	}
	// And Severance is in Up Next as episode 2, the next to watch.
	if it := upNextEntry(ts, "s95396"); it["episode"] != 2.0 || it["reason"] != "next" || it["new_count"] != 1.0 {
		t.Errorf("up next: %+v", it)
	}
}

// upNextEntry is the Up Next entry for a movie, or for a show's episode.
func upNextEntry(ts *testServer, id string) map[string]any {
	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	for _, it := range up {
		if it["id"] == id || it["show_id"] == id {
			return it
		}
	}
	return nil
}

// Up Next mixes all three: part-watched first (by when played, with a head
// start), then the rest by the latest that happened to each.
func TestUpNextReasons(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	// A fresh library: everything is new; Severance as episode 1, of 2.
	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	if len(up) != 3 {
		t.Fatalf("fresh: %+v", up)
	}
	for _, it := range up {
		if it["reason"] != "new" {
			t.Errorf("fresh, not new: %+v", it)
		}
	}
	if it := upNextEntry(ts, "s95396"); it["episode"] != 1.0 || it["new_count"] != 2.0 {
		t.Errorf("Severance: %+v", it)
	}

	// Part-way through the movie: it leads, as "continue".
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	up = nil
	ts.json("GET", "/api/up-next", "", &up)
	if up[0]["id"] != "m238" || up[0]["reason"] != "continue" {
		t.Errorf("continue first: %+v", up)
	}

	// Watched movies and movies taken out don't come back as new.
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/up-next/f7ea74b1c96ec/remove", "", nil).Body.Close()
	if upNextEntry(ts, "m238") != nil || upNextEntry(ts, "f7ea74b1c96ec") != nil {
		t.Errorf("watched or removed movies still in Up Next")
	}

	// 40 days on, nothing's new: Severance goes too, unless started.
	ts.sv.now = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }
	up = nil
	ts.json("GET", "/api/up-next", "", &up)
	if len(up) != 0 {
		t.Errorf("40 days on: %+v", up)
	}
}

// "New" is the last 30 days by default; ?since changes it, ?since=0 is any
// time.
func TestNewWindow(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.sv.now = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }

	var eps []newEpisodeView
	ts.json("GET", "/api/episodes/new", "", &eps)
	if len(eps) != 0 {
		t.Errorf("40 days on, still new: %+v", eps)
	}
	ts.json("GET", "/api/episodes/new?since=0", "", &eps)
	if len(eps) != 1 {
		t.Errorf("since=0: %+v", eps)
	}
	var movies []movieView
	ts.json("GET", "/api/movies?since=30d", "", &movies)
	if len(movies) != 0 {
		t.Errorf("movies 40 days on: %+v", movies)
	}
	ts.json("GET", "/api/movies?since=60d", "", &movies)
	if len(movies) != 2 {
		t.Errorf("movies within 60 days: %+v", movies)
	}
}

func TestMovieFilters(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()

	var movies []movieView
	ts.json("GET", "/api/movies?unwatched=true", "", &movies)
	if len(movies) != 1 || movies[0].ID == "m238" {
		t.Errorf("unwatched: %+v", movies)
	}
	ts.json("GET", "/api/movies", "", &movies)
	if len(movies) != 2 {
		t.Errorf("unfiltered: %+v", movies)
	}
	for _, q := range []string{"?since=soon", "?unwatched=maybe"} {
		if r := ts.do("GET", "/api/movies"+q, "", nil); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", q, r.StatusCode)
		}
	}
	if r := ts.do("GET", "/api/episodes/new?since=soon", "", nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("episodes since=soon: %d", r.StatusCode)
	}
}

// Up Next: a show appears once, as the episode you're part-way through;
// most recently played first. Never a special: part-way through one, the
// show is still offered at its next regular episode.
func TestUpNextOncePerShow(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	special := sh.Seasons[1].Episodes[0].ID

	ts.do("POST", "/api/progress/"+e1, `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	time.Sleep(10 * time.Millisecond)
	ts.do("POST", "/api/progress/"+e2, `{"position": 60, "duration": 1800}`, nil).Body.Close()

	var up []map[string]any
	ts.json("GET", "/api/up-next", "", &up)
	if len(up) != 3 || up[0]["id"] != e2 || up[0]["reason"] != "continue" || up[1]["id"] != "m238" || up[2]["reason"] != "new" {
		t.Errorf("up next: %+v", up)
	}

	time.Sleep(10 * time.Millisecond)
	ts.do("POST", "/api/progress/"+special, `{"position": 60, "duration": 1800}`, nil).Body.Close()
	ts.json("GET", "/api/up-next", "", &up)
	for _, it := range up {
		if it["id"] == special {
			t.Errorf("a special in Up Next: %+v", up)
		}
	}
	if len(up) != 3 || up[0]["id"] != e2 {
		t.Errorf("up next, part-way through a special: %+v", up)
	}
}

// A show's next episode is the same everywhere: Up Next's entry, the
// show's next_episode_id (the Apple app's show page) and the website's
// show page. Part-way through S01E02 with S01E01 unwatched, it's S01E02;
// part-way through a special, it's still a regular episode.
func TestShowNextEpisodeAgrees(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	special := sh.Seasons[1].Episodes[0].ID
	check := func(when, want string) {
		t.Helper()
		var v showView
		ts.json("GET", "/api/shows/s95396", "", &v)
		if v.NextEpisode != want {
			t.Errorf("%s: next_episode_id %q, want %q", when, v.NextEpisode, want)
		}
		var up []map[string]any
		ts.json("GET", "/api/up-next", "", &up)
		for _, it := range up {
			if it["show_id"] == "s95396" && it["id"] != want {
				t.Errorf("%s: Up Next has %v, want %s", when, it["id"], want)
			}
		}
	}

	check("fresh", e1)
	ts.do("POST", "/api/progress/"+e2, `{"position": 60, "duration": 1800}`, nil).Body.Close()
	check("part-way through E02", e2)
	time.Sleep(10 * time.Millisecond)
	ts.do("POST", "/api/progress/"+special, `{"position": 60, "duration": 1800}`, nil).Body.Close()
	check("part-way through a special too", e2)
	ts.do("POST", "/api/progress/"+e2, `{"watched": true}`, nil).Body.Close()
	// After the last regular episode, Up Next has nothing; the page still
	// offers the first unwatched one.
	check("E02 watched, E01 not", e1)
}

// A show with new episodes is prompted from its first unwatched episode,
// however old that is: E02 arrived this week, but E01 (from last month)
// hasn't been watched, so the card is E01.
func TestNewEpisodesStartAtFirstUnwatched(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1 := sh.Seasons[0].Episodes[0]
	ix := ts.sv.ix.Load()
	ix.Episode(e1.ID).Added = time.Now().Add(-60 * 24 * time.Hour)

	var eps []newEpisodeView
	ts.json("GET", "/api/episodes/new", "", &eps)
	if len(eps) != 1 || eps[0].Episode != 1 || eps[0].NewCount != 1 {
		t.Errorf("want E01, with 1 new (E02): %+v", eps)
	}
}

// Removing things from Up Next leaves progress alone: a movie is hidden
// until you play it again; a show (by way of any episode, part-watched or
// not) until you play any of it again.
func TestRemoveFromUpNext(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	ts.do("POST", "/api/progress/"+e1, `{"watched": true}`, nil).Body.Close()

	upIDs := func() map[string]bool {
		var up []map[string]any
		ts.json("GET", "/api/up-next", "", &up)
		ids := map[string]bool{}
		for _, it := range up {
			ids[it["id"].(string)] = true
		}
		return ids
	}
	if ids := upIDs(); !ids["m238"] || !ids[e2] || !ids["f7ea74b1c96ec"] {
		t.Fatalf("before: %v", ids)
	}
	for _, id := range []string{"m238", e2, "f7ea74b1c96ec"} {
		if r := ts.do("POST", "/api/up-next/"+id+"/remove", "", nil); r.StatusCode != http.StatusNoContent {
			t.Fatalf("remove %s: %d", id, r.StatusCode)
		}
	}
	if ids := upIDs(); len(ids) != 0 {
		t.Errorf("after removing all: %v", ids)
	}
	var m movieView
	ts.json("GET", "/api/item/m238", "", &m)
	if m.Position != 600 {
		t.Errorf("movie progress lost: %+v", m)
	}
	var ep episodeView
	ts.json("GET", "/api/item/"+e1, "", &ep)
	if !ep.Watched {
		t.Errorf("hiding the show unwatched its episodes")
	}
	// Playing again brings them back.
	time.Sleep(10 * time.Millisecond)
	ts.do("POST", "/api/progress/"+e2, `{"position": 300, "duration": 3300}`, nil).Body.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 700, "duration": 10500}`, nil).Body.Close()
	if ids := upIDs(); !ids[e2] || !ids["m238"] {
		t.Errorf("not back after playing: %v", ids)
	}
	// A part-watched episode, removed, hides its show too.
	ts.do("POST", "/api/up-next/"+e2+"/remove", "", nil).Body.Close()
	if ids := upIDs(); ids[e2] {
		t.Errorf("part-watched episode still there: %v", ids)
	}
	if r := ts.do("POST", "/api/up-next/nope/remove", "", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
}

// Marking a show unwatched forgets it entirely: taken out of Up Next too,
// so a library can be reset over the API (scripts/reset-library.sh).
func TestShowUnwatchedForgetsRemoval(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1 := sh.Seasons[0].Episodes[0].ID
	ts.do("POST", "/api/progress/"+e1, `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/up-next/"+e1+"/remove", "", nil).Body.Close()
	ts.do("POST", "/api/progress/s95396", `{"watched": false}`, nil).Body.Close()
	if p, ok := ts.sv.state.All()["s95396"]; ok {
		t.Errorf("the show's still kept: %+v", p)
	}
}

// {"position": 0} clears the resume point but keeps watched.
func TestClearPositionKeepsWatched(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 1200, "duration": 10500}`, nil).Body.Close()
	ts.do("POST", "/api/progress/m238", `{"position": 0}`, nil).Body.Close()
	var m movieView
	ts.json("GET", "/api/item/m238", "", &m)
	if m.Position != 0 || !m.Watched {
		t.Errorf("after clearing: %+v", m)
	}
}

// A rewatch: everything watched long ago, then episode 1 again. Next is
// episode 2 (watched or not), not nothing because the finale was watched.
func TestNextAfterRewatch(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	ts.do("POST", "/api/progress/s95396", `{"watched": true}`, nil).Body.Close()
	if it := upNextEntry(ts, "s95396"); it != nil {
		t.Fatalf("all watched, still in Up Next: %+v", it)
	}
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	e1 := sh.Seasons[0].Episodes[0].ID
	st := ts.sv.state // watched again, a while after the rest
	st.set(e1, Progress{Watched: true, Duration: 3300, Updated: time.Now().Add(2 * time.Minute)})
	if it := upNextEntry(ts, "s95396"); it == nil || it["episode"] != 2.0 || it["reason"] != "next" {
		t.Errorf("after rewatching episode 1: %+v", it)
	}
	// Watching the last episode again ends it.
	last := sh.Seasons[0].Episodes[len(sh.Seasons[0].Episodes)-1].ID
	st.set(last, Progress{Watched: true, Updated: time.Now().Add(5 * time.Minute)})
	if it := upNextEntry(ts, "s95396"); it != nil {
		t.Errorf("after the finale: %+v", it)
	}
}

// Each entry says which picture to show, so clients don't work it out: the
// feature's backdrop names it, a row's thumb picks out the episode, and the
// Top Shelf's 2:3 picture is a poster where there is one.
func TestUpNextPictures(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	for _, c := range []struct{ id, backdrop, thumb, shelf string }{
		{"m238", "/art/m238/fanart", "/art/m238/fanart", "/art/m238/poster"},
		{"s95396", "/art/s95396/fanart", "/art/e95396101/thumb", "/art/s95396/season-1"},
	} {
		it := upNextEntry(ts, c.id)
		if it == nil || it["backdrop"] != c.backdrop || it["row_thumb"] != c.thumb || it["shelf_image"] != c.shelf {
			t.Errorf("%s: backdrop %v, row_thumb %v, shelf_image %v", c.id, it["backdrop"], it["row_thumb"], it["shelf_image"])
		}
	}
	if it := upNextEntry(ts, "f7ea74b1c96ec"); it == nil || it["backdrop"] != nil || it["row_thumb"] != nil || it["shelf_image"] != nil {
		t.Errorf("an unscraped movie has no pictures: %+v", it)
	}

	// Whatever's missing, the others stand in.
	for _, c := range []struct {
		view any
		want upNextArt
	}{
		{movieView{Poster: "p"}, upNextArt{"p", "p", "p"}},
		{movieView{Fanart: "f"}, upNextArt{"f", "f", "f"}},
		{movieView{Fanart: "f", Poster: "p"}, upNextArt{"f", "f", "p"}},
		{episodeView{Thumb: "t"}, upNextArt{"t", "t", "t"}},
		{episodeView{Fanart: "f"}, upNextArt{"f", "f", "f"}},
		{episodeView{Thumb: "t", Fanart: "f"}, upNextArt{"f", "t", "f"}},
		{episodeView{Thumb: "t", Fanart: "f", Poster: "p"}, upNextArt{"f", "t", "p"}},
	} {
		if got := upNextPictures(c.view); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.view, got, c.want)
		}
	}
}

// The movie and show lists say what's new, as Up Next has it: a movie added
// in the last 30 days and not watched; a show's new episodes not watched.
func TestListsSayWhatsNew(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()

	var movies []movieView
	ts.json("GET", "/api/movies", "", &movies)
	for _, f := range movies {
		if !f.New {
			t.Errorf("fresh movie not new: %+v", f)
		}
	}
	var shows []showView
	ts.json("GET", "/api/shows", "", &shows)
	if len(shows) != 1 || shows[0].NewCount != 2 {
		t.Fatalf("fresh shows: %+v", shows)
	}

	// Starting a movie takes it off: it's carrying on, not new.
	ts.do("POST", "/api/progress/f7ea74b1c96ec", `{"position": 600, "duration": 5400}`, nil).Body.Close()
	movies = nil
	ts.json("GET", "/api/movies", "", &movies)
	for _, f := range movies {
		if f.New == (f.ID == "f7ea74b1c96ec") {
			t.Errorf("after starting f7ea74b1c96ec: %+v", f)
		}
	}
	ts.do("POST", "/api/progress/f7ea74b1c96ec", `{"position": 0}`, nil).Body.Close()

	// Watching takes it off.
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	ts.do("POST", "/api/progress/m238", `{"watched": true}`, nil).Body.Close()
	ts.do("POST", "/api/progress/"+sh.Seasons[0].Episodes[0].ID, `{"watched": true}`, nil).Body.Close()
	movies, shows = nil, nil
	ts.json("GET", "/api/movies", "", &movies)
	for _, f := range movies {
		if f.New == (f.ID == "m238") {
			t.Errorf("after watching m238: %+v", f)
		}
	}
	ts.json("GET", "/api/shows", "", &shows)
	if shows[0].NewCount != 1 {
		t.Errorf("after watching episode 1: %+v", shows[0])
	}

	// 40 days on, nothing's new.
	ts.sv.now = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }
	movies, shows = nil, nil
	ts.json("GET", "/api/movies", "", &movies)
	ts.json("GET", "/api/shows", "", &shows)
	for _, f := range movies {
		if f.New {
			t.Errorf("40 days on, still new: %+v", f)
		}
	}
	if shows[0].NewCount != 0 {
		t.Errorf("40 days on, still new: %+v", shows[0])
	}
}

// The lists carry what clients sort on: when each thing was added (a
// show's newest episode, specials included) and last watched (a show's
// latest episode).
func TestListsSaySortDates(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var sh showView
	ts.json("GET", "/api/shows/s95396", "", &sh)
	ix := ts.sv.ix.Load()
	day := 24 * time.Hour
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ix.Movie("m238").Added = base
	for i, se := range ix.Show("s95396").Seasons {
		for j, e := range se.Episodes {
			e.Added = base.Add(-time.Duration(10*i+j) * day)
		}
	}
	var special *library.Season
	for _, se := range ix.Show("s95396").Seasons {
		if se.Number == 0 {
			special = se
		}
	}
	if special == nil {
		t.Fatal("no specials in the test show")
	}
	special.Episodes[0].Added = base.Add(5 * day)

	var movies []movieView
	ts.json("GET", "/api/movies", "", &movies)
	if !movies[0].Added.Equal(base) || !movies[0].LastWatched.IsZero() {
		t.Errorf("unwatched movie: added %v, last watched %v", movies[0].Added, movies[0].LastWatched)
	}
	var shows []showView
	ts.json("GET", "/api/shows", "", &shows)
	if !shows[0].Added.Equal(base.Add(5*day)) || !shows[0].LastWatched.IsZero() {
		t.Errorf("unwatched show: added %v, last watched %v", shows[0].Added, shows[0].LastWatched)
	}

	// Watched: the movie, then E02, then E01 (a rewatch).
	e1, e2 := sh.Seasons[0].Episodes[0].ID, sh.Seasons[0].Episodes[1].ID
	ts.do("POST", "/api/progress/m238", `{"position": 600, "duration": 10500}`, nil).Body.Close()
	ts.do("POST", "/api/progress/"+e2, `{"watched": true}`, nil).Body.Close()
	time.Sleep(2 * time.Millisecond)
	ts.do("POST", "/api/progress/"+e1, `{"position": 600, "duration": 3300}`, nil).Body.Close()
	want := ts.sv.state.Get(e1).Played
	if want.IsZero() {
		t.Fatal("a progress report doesn't record when it was played")
	}
	ts.json("GET", "/api/shows", "", &shows)
	if !shows[0].LastWatched.Equal(want) {
		t.Errorf("show last watched %v, want E01's %v", shows[0].LastWatched, want)
	}
	ts.json("GET", "/api/movies", "", &movies)
	played := movies[0].LastWatched
	if played.IsZero() {
		t.Fatal("watched movie has no last_watched")
	}

	// Taking it out of Up Next isn't watching it.
	time.Sleep(2 * time.Millisecond)
	ts.do("POST", "/api/up-next/m238/remove", "", nil).Body.Close()
	ts.json("GET", "/api/movies", "", &movies)
	if !movies[0].LastWatched.Equal(played) {
		t.Errorf("removing from Up Next moved last_watched from %v to %v", played, movies[0].LastWatched)
	}
}
