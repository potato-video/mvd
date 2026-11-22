package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"mvd/internal/config"
	"mvd/internal/library"
)

const progressUsage = `usage: mvd progress [-as NAME] <command>

  list                    what's in Up Next, with how far in
  show NAME|ID            everything kept about a movie or show, episode by
                          episode, and where it stands in Up Next
  clear NAME|ID...        forget it: as if you'd never started it. A movie, an
                          episode, or a whole show
  watched NAME|ID...      mark it watched (a whole show: every episode)

NAME is part of a movie's or show's title; add an episode code for one
episode: mvd progress clear "Star City S01E01". IDs are the ones the API and
'mvd progress list' show. In place of names, clear and watched take

  -movie NAME             only a movie
  -show NAME              only a show
  -show NAME -season N    one season of it (0 for specials)

Each token has its own watch history. This works on the admin's ([server]
token); -as NAME works on the [[server.tokens]] entry with that name, seeing
only what that token can see.

The server keeps the watch history, so mvd server needs to be running.
`

func cmdProgress(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(os.Stderr, progressUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cfg := config.Load()
	api, ok := clientAPI(cfg)
	if !ok {
		return 1
	}
	fs := newFlagSet("mvd progress")
	fs.StringVar(&api.as, "as", "", "work on the watch history of the token named `NAME`")
	fs.Usage = func() { fmt.Fprint(os.Stderr, progressUsage) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if args = fs.Args(); len(args) == 0 {
		fmt.Fprint(os.Stderr, progressUsage)
		return 2
	}
	stateFile := cfg.StateFile
	if api.as != "" {
		// A viewer's progress is kept beside the admin's.
		stateFile = filepath.Join(filepath.Dir(cfg.StateFile), "state-"+config.TokenSlug(api.as)+".json")
	}
	switch args[0] {
	case "list":
		return progressList(api)
	case "show":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: mvd progress show NAME|ID")
			return 2
		}
		if err := progressShow(api, stateFile, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 1
		}
		return 0
	case "clear", "watched":
		return progressSet(api, args[0], args[1:])
	}
	fmt.Fprint(os.Stderr, progressUsage)
	return 2
}

// progressSet runs clear or watched, on names and ids, or on one -movie or
// -show (with -season, one season of it).
func progressSet(api *apiClient, verb string, args []string) int {
	fs := newFlagSet("mvd progress " + verb)
	var movie, show string
	season := -1
	fs.StringVar(&movie, "movie", "", "only the movie whose title contains `NAME`")
	fs.StringVar(&show, "show", "", "only the show whose title contains `NAME`")
	fs.Func("season", "only season `N` of -show (0 for specials)", func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return errors.New("not a season number")
		}
		season = n
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mvd progress %s NAME|ID...\n       mvd progress %[1]s -movie NAME | -show NAME [-season N]\n", verb)
	}
	names, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	flagged := movie != "" || show != ""
	if (movie != "" && show != "") || (season >= 0 && show == "") || flagged == (len(names) > 0) {
		fs.Usage()
		return 2
	}
	watched := verb == "watched"
	var errs []error
	switch {
	case season >= 0:
		errs = append(errs, setSeasonWatched(api, show, season, watched))
	case movie != "":
		errs = append(errs, setWatched(api, movie, "movie", watched))
	case show != "":
		errs = append(errs, setWatched(api, show, "show", watched))
	}
	for _, n := range names {
		errs = append(errs, setWatched(api, n, "", watched))
	}
	rc = 0
	for _, err := range errs {
		if err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			rc = 1
		}
	}
	return rc
}

func progressList(api *apiClient) int {
	var up []struct {
		Kind      string  `json:"kind"`
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Year      int     `json:"year"`
		ShowTitle string  `json:"show_title"`
		Season    int     `json:"season"`
		Episode   int     `json:"episode"`
		Position  float64 `json:"position"`
		Reason    string  `json:"reason"`
	}
	if err := api.get("/api/up-next", &up); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(up) == 0 {
		fmt.Println("nothing in Up Next")
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, it := range up {
		name := library.TitleYear(it.Title, it.Year)
		if it.Kind == "episode" {
			name = fmt.Sprintf("%s S%02dE%02d", it.ShowTitle, it.Season, it.Episode)
			if it.Title != "" {
				name += " " + it.Title
			}
		}
		where := it.Reason // "next" or "new"
		if it.Position > 0 {
			where = clockIn(it.Position)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", it.ID, name, where)
	}
	w.Flush()
	return 0
}

// progressShow prints what the server keeps about a movie or show (read
// from its state file, which says more than the API: when, and whether
// it's been taken out of Up Next), and its entry in Up Next.
func progressShow(api *apiClient, stateFile, arg string) error {
	id, name, err := resolve(api, arg, "")
	if err != nil {
		return err
	}
	type kept struct {
		Position float64   `json:"position"`
		Duration float64   `json:"duration"`
		Watched  bool      `json:"watched"`
		Hidden   bool      `json:"hidden"`
		Updated  time.Time `json:"updated"`
	}
	state := map[string]kept{}
	if b, err := os.ReadFile(stateFile); err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			return fmt.Errorf("%s: %v", stateFile, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// The server writes the file every few seconds, so the last moments
	// may not be in it yet.
	describe := func(k kept, ok bool) string {
		if !ok {
			return "not started"
		}
		var parts []string
		if k.Watched {
			parts = append(parts, "watched")
		}
		if k.Position > 0 {
			parts = append(parts, clockIn(k.Position))
		}
		if k.Hidden {
			parts = append(parts, "taken out of Up Next")
		}
		if len(parts) == 0 {
			parts = append(parts, "-")
		}
		return strings.Join(parts, ", ") + "	" + k.Updated.Local().Format("2 Jan 15:04")
	}

	fmt.Printf("%s  %s\n", name, id)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	var show struct {
		Seasons []struct {
			Episodes []struct {
				ID      string `json:"id"`
				Season  int    `json:"season"`
				Episode int    `json:"episode"`
				Title   string `json:"title"`
			} `json:"episodes"`
		} `json:"seasons"`
	}
	k, ok := state[id]
	if strings.HasPrefix(id, "s") && api.get("/api/shows/"+id, &show) == nil && len(show.Seasons) > 0 {
		if ok && k.Hidden {
			fmt.Fprintf(w, "  the show\t%s\n", describe(k, ok))
		}
		for _, se := range show.Seasons {
			for _, e := range se.Episodes {
				k, ok := state[e.ID]
				fmt.Fprintf(w, "  S%02dE%02d %s\t%s\n", e.Season, e.Episode, e.Title, describe(k, ok))
			}
		}
	} else {
		fmt.Fprintf(w, "  \t%s\n", describe(k, ok))
	}
	w.Flush()

	var up []struct {
		ID       string `json:"id"`
		ShowID   string `json:"show_id"`
		Season   int    `json:"season"`
		Episode  int    `json:"episode"`
		Reason   string `json:"reason"`
		NewCount int    `json:"new_count"`
	}
	if err := api.get("/api/up-next", &up); err != nil {
		return err
	}
	for i, it := range up {
		if it.ID == id || (it.ShowID != "" && it.ShowID == id) {
			where := fmt.Sprintf("Up Next: #%d of %d, %s", i+1, len(up), it.Reason)
			if it.ShowID != "" {
				where += fmt.Sprintf(" (S%02dE%02d)", it.Season, it.Episode)
			}
			fmt.Println(where)
			return nil
		}
	}
	fmt.Printf("Up Next: not in it (%d entries)\n", len(up))
	return nil
}

func clockIn(sec float64) string {
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d in", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d in", s/60, s%60)
}

// setWatched marks a movie, episode or show watched, or clears it back to
// never started (the API's {"watched": false} forgets any position too).
// kind is as for resolve.
func setWatched(api *apiClient, arg, kind string, watched bool) error {
	id, name, err := resolve(api, arg, kind)
	if err != nil {
		return err
	}
	if err := api.post("/api/progress/"+id, map[string]bool{"watched": watched}); err != nil {
		return err
	}
	reportWatched(name, watched)
	return nil
}

// setSeasonWatched does the same for each episode in one season of a show.
// Marked together, they count as watched in one go, so Up Next goes on from
// the season's end.
func setSeasonWatched(api *apiClient, arg string, season int, watched bool) error {
	if m := epCode.FindStringSubmatch(arg); m != nil && strings.TrimSpace(m[1]) != "" {
		return fmt.Errorf("%q is an episode: -season goes with a show's name", arg)
	}
	id, name, err := resolve(api, arg, "show")
	if err != nil {
		return err
	}
	var show struct {
		Seasons []struct {
			Episodes []struct {
				ID          string `json:"id"`
				Season      int    `json:"season"`
				Episode     int    `json:"episode"`
				LastEpisode int    `json:"last_episode"`
			} `json:"episodes"`
		} `json:"seasons"`
	}
	if err := api.get("/api/shows/"+id, &show); err != nil {
		return err
	}
	var ids []string
	count := 0
	for _, se := range show.Seasons {
		for _, e := range se.Episodes {
			if e.Season == season {
				ids = append(ids, e.ID)
				count += max(e.LastEpisode, e.Episode) - e.Episode + 1
			}
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s has no season %d in the library", name, season)
	}
	for _, e := range ids {
		if err := api.post("/api/progress/"+e, map[string]bool{"watched": watched}); err != nil {
			return err
		}
	}
	what := fmt.Sprintf("season %d", season)
	if season == 0 {
		what = "specials"
	}
	if count == 1 {
		reportWatched(fmt.Sprintf("%s %s (1 episode)", name, what), watched)
	} else {
		reportWatched(fmt.Sprintf("%s %s (%d episodes)", name, what, count), watched)
	}
	return nil
}

func reportWatched(name string, watched bool) {
	if watched {
		fmt.Printf("watched: %s\n", name)
	} else {
		fmt.Printf("cleared: %s\n", name)
	}
}

var epCode = regexp.MustCompile(`(?i)^(.*?)[\s._-]*s(\d{1,2})[\s._-]?e(\d{1,3})\s*$`)

// resolve turns what was typed into an item id and a name to report: an id
// as it is, otherwise a movie or show by (part of) its title, and with an
// episode code, that episode of the show. kind "movie" or "show" (-movie or
// -show) looks only among those, and "" among both.
func resolve(api *apiClient, arg, kind string) (id, name string, err error) {
	arg = strings.TrimSpace(arg)
	notFound := func() error {
		switch kind {
		case "movie":
			return fmt.Errorf("no movie called %q (for a show, use -show)", arg)
		case "show":
			return fmt.Errorf("no show called %q (for a movie, use -movie)", arg)
		}
		return fmt.Errorf("nothing in the library called %q", arg)
	}
	var item struct {
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		ShowTitle string `json:"show_title"`
		Season    int    `json:"season"`
		Episode   int    `json:"episode"`
	}
	if !strings.Contains(arg, " ") && api.get("/api/item/"+arg, &item) == nil {
		// -show takes a show's id, or an episode's, as it takes an episode code.
		if kind == "movie" && item.Kind != "movie" || kind == "show" && item.Kind == "movie" {
			return "", "", notFound()
		}
		if item.ShowTitle != "" {
			return arg, fmt.Sprintf("%s S%02dE%02d", item.ShowTitle, item.Season, item.Episode), nil
		}
		return arg, item.Title, nil
	}

	title, season, episode := arg, -1, -1
	if m := epCode.FindStringSubmatch(arg); kind != "movie" && m != nil && strings.TrimSpace(m[1]) != "" {
		title, season, episode = m[1], atoi(m[2]), atoi(m[3])
	}

	type entry struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Title string `json:"title"`
		Year  int    `json:"year"`
	}
	var all, shows []entry
	if season < 0 && kind != "show" {
		if err := api.get("/api/movies", &all); err != nil {
			return "", "", err
		}
	}
	if kind != "movie" {
		if err := api.get("/api/shows", &shows); err != nil {
			return "", "", err
		}
	}
	all = append(all, shows...)

	want := library.NormTitle(title)
	var exact, partial []entry
	for _, e := range all {
		n := library.NormTitle(e.Title)
		switch {
		case n == want:
			exact = append(exact, e)
		case strings.Contains(n, want):
			partial = append(partial, e)
		}
	}
	found := exact
	if len(found) == 0 {
		found = partial
	}
	switch {
	case len(found) == 0 && season >= 0:
		return "", "", fmt.Errorf("no show called %q", title)
	case len(found) == 0:
		return "", "", notFound()
	case len(found) > 1:
		var names []string
		for _, e := range found {
			names = append(names, fmt.Sprintf("%s (%s)", library.TitleYear(e.Title, e.Year), e.ID))
		}
		return "", "", fmt.Errorf("%q could be %s: say which, or use an id", title, strings.Join(names, ", "))
	}
	hit := found[0]
	if season < 0 {
		return hit.ID, library.TitleYear(hit.Title, hit.Year), nil
	}

	var show struct {
		Seasons []struct {
			Episodes []struct {
				ID          string `json:"id"`
				Season      int    `json:"season"`
				Episode     int    `json:"episode"`
				LastEpisode int    `json:"last_episode"`
			} `json:"episodes"`
		} `json:"seasons"`
	}
	if err := api.get("/api/shows/"+hit.ID, &show); err != nil {
		return "", "", err
	}
	for _, se := range show.Seasons {
		for _, e := range se.Episodes {
			if e.Season == season && (e.Episode == episode || (e.Episode < episode && episode <= e.LastEpisode)) {
				return e.ID, fmt.Sprintf("%s S%02dE%02d", hit.Title, season, episode), nil
			}
		}
	}
	return "", "", fmt.Errorf("%s has no S%02dE%02d", hit.Title, season, episode)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// apiClient talks to the mvd server on this machine.
type apiClient struct {
	base, token string
	as          string // act as the token with this name (X-Mvd-As), if set
	hint        string // added to "is it running?"
	http        *http.Client
}

func localAPI(cfg config.Config) *apiClient {
	return &apiClient{base: localServer(cfg), token: cfg.Token, hint: " (it keeps the watch history)",
		http: &http.Client{Timeout: 30 * time.Second}}
}

func (a *apiClient) get(path string, out any) error { return a.do("GET", path, nil, out) }

func (a *apiClient) post(path string, body any) error { return a.do("POST", path, body, nil) }

func (a *apiClient) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if a.as != "" {
		req.Header.Set("X-Mvd-As", a.as)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return errors.New("can't reach mvd server at " + a.base + ": is it running?" + a.hint)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("the server wants a token: set token under [server] in config.toml to match it")
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("not found: " + path)
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusForbidden:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return errors.New(strings.TrimSpace(string(b))) // the server says what's wrong
	case resp.StatusCode != http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
