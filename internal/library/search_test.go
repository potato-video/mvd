package library

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"mvd/internal/testutil"
)

func TestFold(t *testing.T) {
	cases := map[string]string{
		"The Godfather Part II":       "the godfather part ii",
		"Amélie":                      "amelie",
		"Ame\u0301lie":                "amelie", // decomposed, as a Mac names files
		"Ocean's Eleven":              "oceans eleven",
		"Ocean’s Eleven":              "oceans eleven",
		"WALL·E":                      "wall e",
		"  Mission: Impossible -- 2 ": "mission impossible 2",
		"Fast & Furious":              "fast and furious",
		"Straße":                      "strasse",
		"Æon Flux":                    "aeon flux",
		"Cœur fidèle":                 "coeur fidele",
		"Smørrebrød":                  "smorrebrod",
		"Łódź Ďáblova Żona":           "lodz dablova zona",
		"ÀÉÎÕÜ Ñ Ç Ý Þ":               "aeiou n c y th",
		"Ĳsselmeer ŉ ſ":               "ijsselmeer n s",
		"Москва":                      "москва",
		"...":                         "",
		"":                            "",
	}
	for in, want := range cases {
		if got := string(appendFolded(nil, in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if len(latinA) != 0x80 {
		t.Errorf("latinA has %d letters, want 128", len(latinA))
	}
}

// searchIndex is a small library, in title order as BuildIndex leaves it.
func searchIndex() *Index {
	movie := func(title string, year int, set string, directors ...string) *Movie {
		return &Movie{ID: "m-" + title, Title: title, SortTitle: NormTitle(title), Year: year, Set: set, Directors: directors}
	}
	show := func(title string, year int, rating string, eps ...string) *Show {
		sh := &Show{ID: "s-" + title, Title: title, Year: year, Rating: rating}
		se := &Season{Number: 1}
		for i, e := range eps {
			se.Episodes = append(se.Episodes, &Episode{ID: fmt.Sprintf("e-%s-%d", title, i+1), Show: sh,
				Season: 1, Episodes: []int{i + 1}, Title: e})
		}
		sh.Seasons = []*Season{se}
		return sh
	}
	return &Index{
		Movies: []*Movie{
			movie("Alien", 1979, "Alien Collection", "Ridley Scott"),
			movie("Aliens", 1986, "Alien Collection", "James Cameron"),
			movie("Amélie", 2001, "", "Jean-Pierre Jeunet"),
			movie("Blade Runner 2049", 2017, "", "Denis Villeneuve"),
			movie("The Conversation", 1974, "", "Francis Ford Coppola"),
			movie("God Bless America", 2011, ""),
			movie("The Godfather", 1972, "The Godfather Collection", "Francis Ford Coppola"),
			movie("The Godfather Part II", 1974, "The Godfather Collection", "Francis Ford Coppola"),
			movie("My Godfather", 1999, ""),
			movie("Ocean's Eleven", 2001, "Ocean's Collection", "Steven Soderbergh"),
			movie("Only God Forgives", 2013, ""),
		},
		Shows: []*Show{
			show("Alien: Earth", 2025, "TV-MA", "Neverland", "Mr. October"),
			show("The Sopranos", 1999, "TV-MA", "The Sopranos", "46 Long", "Pine Barrens"),
			show("Twin Peaks", 1990, "TV-14", "Pilot", "Traces to Nowhere", "The Pine Weasel"),
		},
	}
}

func titles(r SearchResults) (movies, shows, eps string) {
	var m, s, e []string
	for _, x := range r.Movies {
		m = append(m, x.Title)
	}
	for _, x := range r.Shows {
		s = append(s, x.Title)
	}
	for _, x := range r.Episodes {
		e = append(e, x.Show.Title+": "+x.Title)
	}
	return strings.Join(m, " | "), strings.Join(s, " | "), strings.Join(e, " | ")
}

func TestSearch(t *testing.T) {
	ix := searchIndex()
	cases := []struct{ q, movies, shows, eps string }{
		{"", "", "", ""},
		{" ' ", "", "", ""},
		{"zzz", "", "", ""},
		// Case, accents and punctuation don't count.
		{"amelie", "Amélie", "", ""},
		{"AMÉLIE", "Amélie", "", ""},
		{"oceans eleven", "Ocean's Eleven", "", ""},
		{"ocean's", "Ocean's Eleven", "", ""},
		// Every word starts a word, in any order; the middle of a word doesn't count.
		{"god fa", "The Godfather | The Godfather Part II | My Godfather", "", ""},
		{"ii god", "The Godfather Part II", "", ""},
		{"father", "", "", ""},
		// Words typed apart can be one word of the title, from its start.
		{"god father", "The Godfather | The Godfather Part II | My Godfather", "", ""},
		{"fa god", "", "", ""},
		// Exact, then starts with, then words, then elsewhere; ties in title order.
		{"godfather", "The Godfather | The Godfather Part II | My Godfather", "", ""},
		{"god", "God Bless America | The Godfather | The Godfather Part II | My Godfather | Only God Forgives", "", ""},
		{"alien", "Alien | Aliens", "Alien: Earth", "Alien: Earth: Neverland | Alien: Earth: Mr. October"},
		{"aliens", "Aliens", "", ""},
		// A collection finds all of it; a director finds their movies.
		{"godfather coll", "The Godfather | The Godfather Part II", "", ""},
		{"coppola", "The Conversation | The Godfather | The Godfather Part II", "", ""},
		{"coppola conv", "The Conversation", "", ""},
		{"ford fran", "The Conversation | The Godfather | The Godfather Part II", "", ""},
		// A year, or a number in a title.
		{"alien 1979", "Alien", "", ""},
		{"2049", "Blade Runner 2049", "", ""},
		{"1974", "The Conversation | The Godfather Part II", "", ""},
		{"1999", "My Godfather", "The Sopranos", ""},
		// Episodes: on their own title, then (lower) on their show's.
		{"pine", "", "", "The Sopranos: Pine Barrens | Twin Peaks: The Pine Weasel"},
		{"sopranos", "", "The Sopranos", "The Sopranos: The Sopranos | The Sopranos: 46 Long | The Sopranos: Pine Barrens"},
		{"sopranos pine", "", "", "The Sopranos: Pine Barrens"},
		{"pilot", "", "", "Twin Peaks: Pilot"},
	}
	for _, c := range cases {
		m, s, e := titles(ix.Search(c.q, SearchLimit, nil))
		if m != c.movies || s != c.shows || e != c.eps {
			t.Errorf("%q:\n  movies   %q\n  want     %q\n  shows    %q\n  want     %q\n  episodes %q\n  want     %q",
				c.q, m, c.movies, s, c.shows, e, c.eps)
		}
	}
}

func TestSearchAllowed(t *testing.T) {
	ix := searchIndex()
	kids := func(rating string) bool { return rating == "TV-14" }
	m, s, e := titles(ix.Search("pine", SearchLimit, kids))
	if m != "" || s != "" || e != "Twin Peaks: The Pine Weasel" {
		t.Errorf("pine: %q, %q, %q", m, s, e)
	}
	if _, s, e := titles(ix.Search("sopranos", SearchLimit, kids)); s != "" || e != "" {
		t.Errorf("sopranos: %q, %q", s, e)
	}
}

// bigIndex is a made-up library the size the notes ask to measure: 2,000
// movies, 200 shows, 20,000 episodes.
func bigIndex() *Index {
	words := strings.Fields("the night long day étoile king return of dark river señor house last über café " +
		"man woman story city under moon blood summer winter ghost")
	title := func(n int) string {
		return fmt.Sprintf("%s %s %s %d", words[n%len(words)], words[n/3%len(words)], words[n/7%len(words)], n)
	}
	ix := &Index{}
	for i := range 2000 {
		ix.Movies = append(ix.Movies, &Movie{Title: title(i), Year: 1950 + i%75, Set: title(i/4) + " Collection",
			Directors: []string{"Agnès " + words[i%len(words)]}})
	}
	for i := range 200 {
		sh := &Show{Title: title(i * 11), Year: 1990 + i%35}
		for s := range 5 {
			se := &Season{Number: s + 1}
			for e := range 20 {
				se.Episodes = append(se.Episodes, &Episode{Show: sh, Season: s + 1, Episodes: []int{e + 1}, Title: title(i*100 + s*20 + e)})
			}
			sh.Seasons = append(sh.Seasons, se)
		}
		ix.Shows = append(ix.Shows, sh)
	}
	return ix
}

// A one-letter query matches thousands of titles, and is capped.
func TestSearchLimit(t *testing.T) {
	r := bigIndex().Search("t", SearchLimit, nil)
	if len(r.Movies) != SearchLimit || len(r.Shows) == 0 || len(r.Shows) > SearchLimit || len(r.Episodes) != SearchLimit {
		t.Errorf("%d movies, %d shows, %d episodes; want %d at most", len(r.Movies), len(r.Shows), len(r.Episodes), SearchLimit)
	}
	// The best 40, not the first 40: a title starting with what's typed
	// comes before one that only has the word in it, however late it is.
	for _, m := range r.Movies {
		if !strings.HasPrefix(m.Title, "the ") {
			t.Errorf("%q made the first %d", m.Title, SearchLimit)
		}
	}
}

// A search's garbage doesn't grow with the library: the query and the
// results, not a folded copy of every title.
func TestSearchGarbage(t *testing.T) {
	if testutil.Race {
		t.Skip("the race detector allocates as it goes")
	}
	ix := bigIndex()
	for _, q := range []string{"t", "dark river", "zzz", "agnès king 1979"} {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		ix.Search(q, SearchLimit, nil)
		runtime.ReadMemStats(&b)
		if n := b.TotalAlloc - a.TotalAlloc; n > 16<<10 {
			t.Errorf("%q: %d bytes of garbage over 22,200 titles", q, n)
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	ix := bigIndex()
	for _, q := range []string{"t", "dark river", "zzz"} {
		b.Run(q, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ix.Search(q, SearchLimit, nil)
			}
		})
	}
}
