//go:build webui

package server

import (
	"net/http"
	"slices"
	"strings"
)

// Sorting the Movies and TV grids, as the Apple apps do (MVDKit's
// LibraryOrder): the same sorts, words, directions and tie-breaks. The API
// leaves sorting to its clients; the website is rendered here, so it sorts
// here.

// gridSort is one way a grid can be sorted: its word in ?sort= and the
// cookie, and its name in the select.
type gridSort struct{ Word, Label string }

var (
	movieSorts = []gridSort{{"title", "Alphabetically"}, {"year", "Year"}, {"added", "Date Added"},
		{"watched", "Last Watched"}, {"runtime", "Runtime"}}
	// Shows have no single runtime.
	showSorts = []gridSort{{"title", "Alphabetically"}, {"year", "Year"}, {"added", "Date Added"},
		{"watched", "Last Watched"}, {"unwatched", "Unwatched Episodes"}}
)

// gridOrder is a sort and whether it's reversed from its natural direction:
// "added", or "-added" reversed.
type gridOrder struct {
	Sort     string
	Reversed bool
}

// parseOrder reads a grid's order, if it's one of that grid's sorts.
func parseOrder(s string, sorts []gridSort) (gridOrder, bool) {
	o := gridOrder{Sort: strings.TrimPrefix(s, "-"), Reversed: strings.HasPrefix(s, "-")}
	for _, g := range sorts {
		if g.Word == o.Sort {
			return o, true
		}
	}
	return gridOrder{Sort: "title"}, false
}

func (o gridOrder) String() string {
	if o.Reversed {
		return "-" + o.Sort
	}
	return o.Sort
}

func (o gridOrder) reverse() gridOrder { return gridOrder{o.Sort, !o.Reversed} }

// descending: the list runs Z to A, newest or most first, or longest first.
// Naturally it's A to Z, shortest first, and otherwise newest or most first.
func (o gridOrder) descending() bool { return (o.Sort != "runtime") != o.Reversed }

// sortGrid puts items, in title order, in this order. key gives an item's
// value for a sort, false when it has none (never watched, no year): those
// go last, in title order, either way. Ties keep the title order.
func sortGrid[T any](items []T, o gridOrder, key func(T, string) (int64, bool)) []T {
	if o.Sort == "title" {
		if o.Reversed {
			slices.Reverse(items)
		}
		return items
	}
	type keyed struct {
		k  int64
		it T
	}
	var have []keyed
	var missing []T
	for _, it := range items {
		if k, ok := key(it, o.Sort); ok {
			have = append(have, keyed{k, it})
		} else {
			missing = append(missing, it)
		}
	}
	desc := o.descending()
	slices.SortStableFunc(have, func(a, b keyed) int {
		switch {
		case a.k == b.k:
			return 0
		case (a.k < b.k) != desc:
			return -1
		}
		return 1
	})
	out := items[:0]
	for _, h := range have {
		out = append(out, h.it)
	}
	return append(out, missing...)
}

func movieKey(m movieView, sort string) (int64, bool) {
	switch sort {
	case "year":
		return int64(m.Year), m.Year > 0
	case "added":
		return m.Added.UnixNano(), !m.Added.IsZero()
	case "watched":
		return m.LastWatched.UnixNano(), !m.LastWatched.IsZero()
	case "runtime":
		return int64(m.Runtime), m.Runtime > 0
	}
	return 0, false
}

func showKey(s showView, sort string) (int64, bool) {
	switch sort {
	case "year":
		return int64(s.Year), s.Year > 0
	case "added":
		return s.Added.UnixNano(), !s.Added.IsZero()
	case "watched":
		return s.LastWatched.UnixNano(), !s.LastWatched.IsZero()
	case "unwatched":
		return int64(s.Unwatched), true
	}
	return 0, false
}

// sortBar is the select over a grid, and the arrow beside it that reverses
// the order.
type sortBar struct {
	Path       string // "/movies"
	Options    []sortOption
	Reverse    string // the link that reverses the order
	Descending bool   // the arrow points down
}

type sortOption struct {
	Word, Label string
	Selected    bool
}

// gridOrderFor is the order a grid's request asks for: ?sort=, which is
// then remembered in a cookie (one per grid), else the cookie, else by
// title.
func gridOrderFor(w http.ResponseWriter, r *http.Request, grid string, sorts []gridSort) gridOrder {
	name := "mvd_sort_" + grid
	if o, ok := parseOrder(r.URL.Query().Get("sort"), sorts); ok {
		http.SetCookie(w, &http.Cookie{Name: name, Value: o.String(), Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
		return o
	}
	if c, err := r.Cookie(name); err == nil {
		o, _ := parseOrder(c.Value, sorts)
		return o
	}
	return gridOrder{Sort: "title"}
}

func newSortBar(path string, o gridOrder, sorts []gridSort) *sortBar {
	b := &sortBar{Path: path, Reverse: path + "?sort=" + o.reverse().String(), Descending: o.descending()}
	for _, g := range sorts {
		b.Options = append(b.Options, sortOption{g.Word, g.Label, g.Word == o.Sort})
	}
	return b
}
