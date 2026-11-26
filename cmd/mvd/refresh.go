package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvd/internal/config"
	"mvd/internal/library"
	"mvd/internal/tmdb"
)

// cmdRefresh works on the library in place: "movies", "tv", or both.
func cmdRefresh(args []string) int {
	kinds, args, err := refreshKinds(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 2
	}
	single := len(kinds) == 1

	fs := newFlagSet("mvd refresh")
	var o library.Options
	fs.BoolVar(&o.DryRun, "n", false, "dry run: show what would happen, change nothing")
	yes := fs.Bool("y", false, "no prompts: accept confident matches, skip the rest")
	fs.BoolVar(&o.Refresh, "all", false, "re-fetch metadata for everything, not just what's missing")
	fs.BoolVar(&o.Force, "force", false, "ignore existing NFOs, match again and overwrite artwork")
	fs.BoolVar(&o.NoRename, "no-rename", false, "leave file and folder names alone")
	fs.StringVar(&o.Name, "movie", "", "only the movie whose name contains `NAME`")
	fs.StringVar(&o.Name, "show", "", "only the show whose name contains `NAME`")
	fs.Func("season", "TV: only season `N` of -show (0 for specials)", func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("not a season number")
		}
		o.Season = &n
		return nil
	})
	fs.StringVar(&o.Order, "order", "", "TV: episode order for -show, remembered in its .mvd:\n"+
		"aired, dvd, production, digital, absolute, tv, pick (choose from a list),\nor a TMDB episode group id")
	lang := fs.String("lang", "", "metadata language (overrides config)")
	region := fs.String("region", "", "certification country (overrides config)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mvd refresh [movies|tv] [flags] [DIR]")
		fs.PrintDefaults()
	}

	pos, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	if len(pos) > 1 || (len(pos) == 1 && !single) {
		fs.Usage()
		if !single {
			fmt.Fprintln(os.Stderr, "(DIR needs 'movies' or 'tv' before it)")
		}
		return 2
	}
	if o.Season != nil && (o.Name == "" || o.Order != "") {
		fmt.Fprintln(os.Stderr, "mvd: -season needs -show, and can't go with -order, which files the whole show again")
		return 2
	}
	if o.Order != "" {
		if o.Name == "" || !library.ValidOrder(o.Order) {
			fmt.Fprintln(os.Stderr, "mvd: -order needs -show, and one of aired, dvd, production, digital, absolute, tv, pick or a group id")
			return 2
		}
		o.Refresh = true // re-file the show's episodes under the new numbering
	}

	cfg := config.Load()
	if *lang != "" {
		cfg.Language = *lang
	}
	if *region != "" {
		cfg.Region = *region
	}
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, noAPIKey())
		return 2
	}
	dir := ""
	if len(pos) == 1 {
		dir = pos[0]
	}
	rc = 0
	for _, tv := range kinds {
		if r := refreshKind(cfg, o, !*yes, tv, single, dir); r != 0 {
			rc = r
		}
		if rc == 2 {
			break
		}
	}
	return rc
}

// refreshKinds works out which libraries a refresh covers (false for movies,
// true for TV): the ones named first, or implied by the flags, -movie for
// movies and -show, -season or -order for TV. It returns the arguments
// after the name.
func refreshKinds(args []string) ([]bool, []string, error) {
	named := ""
	if len(args) > 0 && (args[0] == "movies" || args[0] == "tv") {
		named, args = args[0], args[1:]
	}
	var movieFlag, tvFlag string
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		switch name {
		case "movie":
			movieFlag = "-movie"
		case "show", "season", "order":
			if tvFlag == "" {
				tvFlag = "-" + name
			}
		}
	}
	switch {
	case movieFlag != "" && tvFlag != "":
		return nil, nil, fmt.Errorf("-movie is for movies and %s for TV: use one or the other", tvFlag)
	case movieFlag != "" && named == "tv":
		return nil, nil, fmt.Errorf("-movie is for movies, not tv (for a show, use -show)")
	case tvFlag != "" && named == "movies":
		return nil, nil, fmt.Errorf("%s is for TV, not movies (for a movie, use -movie)", tvFlag)
	case movieFlag != "" || named == "movies":
		return []bool{false}, args, nil
	case tvFlag != "" || named == "tv":
		return []bool{true}, args, nil
	}
	return []bool{false, true}, args, nil
}

// refreshKind refreshes the movies or the TV, in dir or the configured folder.
func refreshKind(cfg config.Config, o library.Options, ask, tv, single bool, dir string) int {
	kind, key := "movies", "movies_dir"
	if tv {
		kind, key = "tv", "tv_dir"
	}
	if dir == "" {
		dir = cfg.MoviesDir
		if tv {
			dir = cfg.TVDir
		}
	}
	if dir == "" {
		fmt.Fprintf(os.Stderr, "mvd: no %s folder: pass DIR, or set video_dir or %s under [paths] in ~/.config/mvd/config.toml\n", kind, key)
		return 2
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		if !single && os.IsNotExist(err) {
			return 0 // no movies (or no TV) yet: nothing to refresh
		}
		fmt.Fprintf(os.Stderr, "mvd: %s is not a directory\n", root)
		return 1
	}
	if !single {
		fmt.Printf("— %s —\n", map[bool]string{false: "Movies", true: "TV"}[tv])
	}

	tm := tmdb.New(cfg)
	tm.PruneCache()
	s := library.NewRefresher(o, cfg, tm, root, tv)
	if ask {
		s.SetAsk(library.AskTerminal(os.Stdin, os.Stdout))
	}
	var rc int
	if tv {
		rc = s.RunTV()
	} else {
		rc = s.Run()
	}
	if s.Stats().Changed > 0 && !o.DryRun {
		pingServer(cfg)
	}
	return rc
}
