package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"mvd/internal/config"
)

const searchUsage = `usage: mvd search [-server URL] [-token T] [-json] WORDS...

Search the library, as the website and the Apple TV app do: movies, shows and
episodes by title, a movie's collection or director, or a year. Every word
must start a word ("god fa" finds The Godfather); case, accents and
punctuation don't count. At most 40 of each, best first.

Prints a line each: what it is, its name, and its id, which the other
commands take (mvd progress watched ID).

  -server URL   the server; default: [client] server in config.toml, else this
                machine's, else the one found on the local network
  -token T      its token; default: [server] token in config.toml
  -json         print the server's JSON as it is
`

// What mvd search prints of GET /api/search.
type searchResults struct {
	Movies []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"movies"`
	Shows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"shows"`
	Episodes []struct {
		ID          string `json:"id"`
		ShowTitle   string `json:"show_title"`
		Season      int    `json:"season"`
		Episode     int    `json:"episode"`
		LastEpisode int    `json:"last_episode"`
		Title       string `json:"title"`
	} `json:"episodes"`
}

func cmdSearch(args []string) int {
	cfg := config.Load()
	fs := newFlagSet("mvd search")
	fs.Usage = func() { fmt.Fprint(os.Stderr, searchUsage) }
	server := fs.String("server", "", "")
	token := fs.String("token", cfg.Token, "")
	asJSON := fs.Bool("json", false, "")
	words, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	if len(words) == 0 {
		fmt.Fprint(os.Stderr, searchUsage)
		return 2
	}
	if *server == "" {
		found, ok := findServer(cfg, os.Stderr)
		if !ok {
			return 1
		}
		*server = found
	}
	api := &apiClient{base: strings.TrimRight(*server, "/"), token: *token, http: &http.Client{Timeout: 30 * time.Second}}
	path := "/api/search?q=" + url.QueryEscape(strings.Join(words, " "))
	if *asJSON {
		var raw json.RawMessage
		if err := api.get(path, &raw); err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 1
		}
		fmt.Println(string(raw))
		return 0
	}
	var res searchResults
	if err := api.get(path, &res); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	out := searchReport(res)
	if out == "" {
		fmt.Fprintln(os.Stderr, "mvd: nothing found")
		return 1
	}
	fmt.Print(out)
	return 0
}

// searchReport is a line for each thing found: its kind, its name (a
// movie's or show's with the year, an episode's with its show and code),
// and its id, lined up.
func searchReport(res searchResults) string {
	type line struct{ kind, name, id string }
	var lines []line
	titled := func(title string, year int) string {
		if year == 0 {
			return title
		}
		return fmt.Sprintf("%s (%d)", title, year)
	}
	for _, m := range res.Movies {
		lines = append(lines, line{"movie", titled(m.Title, m.Year), m.ID})
	}
	for _, s := range res.Shows {
		lines = append(lines, line{"show", titled(s.Title, s.Year), s.ID})
	}
	for _, e := range res.Episodes {
		code := fmt.Sprintf("S%02dE%02d", e.Season, e.Episode)
		if e.LastEpisode > 0 {
			code += fmt.Sprintf("-E%02d", e.LastEpisode)
		}
		lines = append(lines, line{"episode", strings.TrimSpace(e.ShowTitle + " " + code + " " + e.Title), e.ID})
	}
	width := 0
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l.name))
	}
	width = min(width, 60)
	var b strings.Builder
	for _, l := range lines {
		pad := max(width-utf8.RuneCountInString(l.name), 0)
		fmt.Fprintf(&b, "%-7s  %s%s  %s\n", l.kind, l.name, strings.Repeat(" ", pad), l.id)
	}
	return b.String()
}
