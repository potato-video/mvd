package library

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Reading an import item: what's in it (movies, a show's episodes, extras,
// subtitles, archives) and its .mvd hint, without asking TMDB.

// A unit is one thing to match: a movie (perhaps in parts), or one show's
// episodes from an item.
type movieJob struct {
	label  string
	parsed Parsed
	alt    string
	videos []string
	extras []string // folders and files to move in beside the movie
	tmdb   int      // from a hint
}

type showJob struct {
	label  string
	parsed Parsed
	alt    string
	eps    []*epFile
	tmdb   int
	order  string
}

// itemPlan is how mvd reads one top-level entry of the import folder.
type itemPlan struct {
	movies   []*movieJob
	shows    []*showJob
	unknown  []string // videos it can't place
	tvExtras []string // extras folders in a TV item
	videos   []string // every video that matters (not samples)
	archives bool     // archives of any kind, unpackable or not
	firsts   []string // archives mvd can unpack: zips, and each RAR set's first volume
	subs     map[string][]extraSub
}

type extraSub struct {
	path   string
	suffix string // ".en.srt"
}

type hint struct {
	kind  string // "movie" or "tv"
	tmdb  int
	order string
	path  string
}

var (
	// Jellyfin's convention is a hyphen ("Movie (2010)-trailer"): with a dot or
	// space allowed too, "The.Big.Short" would count as an extra.
	extrasSuffix = regexp.MustCompile(`(?i)-(trailer|featurette|behindthescenes|deleted|deletedscene|interview|scene|short|extra|other)\d*$`)
	partNumRe    = regexp.MustCompile(`(?i)(?:^|[^a-z])part[ ._-]?(\d{1,2})(?:[^0-9]|$)`)
	subLeadNum   = regexp.MustCompile(`^\d+[_ .-]+`)
	archiveRe    = regexp.MustCompile(`(?i)\.(rar|r\d\d|7z|zip|\d{3})$`)
)

var samplesDirs = map[string]bool{"sample": true, "samples": true, "proof": true, "screens": true, "screenshots": true}

func isSubsDir(name string) bool {
	switch strings.ToLower(name) {
	case "subs", "sub", "subtitles", "subtitle":
		return true
	}
	return false
}

func isSample(p string, size int64) bool {
	base := strings.ToLower(videoBase(p))
	return (base == "sample" || strings.Contains(base, "-sample") || strings.Contains(base, ".sample") ||
		strings.HasPrefix(base, "sample-") || strings.HasPrefix(base, "sample.")) && size < 400<<20
}

type movieCand struct {
	path string
	dirs []string
}

type foundVideo struct {
	path    string
	dirs    []string // folder names from the item down to the file's folder
	season  int      // from those folders, or -1
	isExtra bool
}

// planItem walks an item and works out what's in it.
func planItem(root string, isDir bool, kind string) *itemPlan {
	p := &itemPlan{subs: map[string][]extraSub{}}
	var found []foundVideo
	var subs []string

	visit := func(path string, dirs []string, size int64) {
		name := filepath.Base(path)
		// Video first: an extensionless video named "Show.S01E01.101" looks
		// like a split archive's ".101" by name alone.
		switch {
		case isSubtitle(path):
			subs = append(subs, path)
			return
		case isVideo(path):
		case archiveRe.MatchString(name):
			p.archives = true
			if isArchive(name) && archiveFirst(name) {
				p.firsts = append(p.firsts, path)
			}
			return
		default:
			return
		}
		if isSample(path, size) {
			return
		}
		fs := -1
		for _, d := range dirs {
			if n := folderSeason(d); n >= 0 {
				fs = n
			}
		}
		found = append(found, foundVideo{path: path, dirs: dirs, season: fs,
			isExtra: kind != "tv" && extrasSuffix.MatchString(videoBase(path))})
	}

	if !isDir {
		st, _ := os.Stat(root)
		var size int64
		if st != nil {
			size = st.Size()
		}
		visit(root, nil, size)
	} else {
		var walk func(dir string, dirs []string)
		walk = func(dir string, dirs []string) {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				name := e.Name()
				if skipName(name) {
					continue
				}
				path := filepath.Join(dir, name)
				if e.IsDir() {
					low := strings.ToLower(cleanRunes(name))
					switch {
					case samplesDirs[low]:
						continue
					case isExtrasDir(name):
						if hasVideos(path) {
							p.tvExtras = append(p.tvExtras, path)
						}
						continue
					}
					walk(path, append(append([]string(nil), dirs...), name))
					continue
				}
				if !e.Type().IsRegular() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				visit(path, dirs, info.Size())
			}
		}
		walk(root, []string{filepath.Base(root)})
	}

	// Sort the videos into episodes, movies and puzzles.
	var movies []movieCand
	var extraFiles []string
	groups := map[string]*showJob{}
	var order []string
	for _, v := range found {
		p.videos = append(p.videos, v.path)
		if v.isExtra {
			extraFiles = append(extraFiles, v.path)
			continue
		}
		name := filepath.Base(v.path)
		season, eps, ok := 0, []int(nil), false
		if kind != "movie" {
			season, eps, ok = parseEpisode(name, v.season)
			if !ok && kind == "tv" {
				season, eps, ok = forcedEpisode(name, v.season)
			}
		}
		if !ok {
			if kind == "tv" {
				p.unknown = append(p.unknown, v.path)
			} else {
				movies = append(movies, movieCand{v.path, v.dirs})
			}
			continue
		}
		parsed, alt, ok := episodeShow(name, v.dirs)
		if !ok {
			p.unknown = append(p.unknown, v.path)
			continue
		}
		key := fmt.Sprintf("%s|%d|%s", NormTitle(parsed.Title), parsed.Year, parsed.Country)
		g := groups[key]
		if g == nil {
			g = &showJob{label: filepath.Base(root), parsed: parsed, alt: alt}
			groups[key] = g
			order = append(order, key)
		}
		g.eps = append(g.eps, &epFile{v.path, season, eps})
	}
	for _, k := range order {
		p.shows = append(p.shows, groups[k])
	}

	// Movies: one per video, except cd1/cd2 stacks, which are one movie.
	byDir := map[string][]movieCand{}
	var dirOrder []string
	for _, f := range movies {
		d := filepath.Dir(f.path)
		if byDir[d] == nil {
			dirOrder = append(dirOrder, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	for _, d := range dirOrder {
		var parts, singles []movieCand
		for _, f := range byDir[d] {
			if parseMovieName(filepath.Base(f.path)).Part > 0 {
				parts = append(parts, f)
			} else {
				singles = append(singles, f)
			}
		}
		var jobs [][]movieCand
		if len(parts) >= 2 {
			sort.Slice(parts, func(i, j int) bool {
				return parseMovieName(filepath.Base(parts[i].path)).Part < parseMovieName(filepath.Base(parts[j].path)).Part
			})
			jobs = append(jobs, parts)
		} else {
			singles = append(singles, parts...)
		}
		for _, f := range singles {
			jobs = append(jobs, []movieCand{f})
		}
		alone := len(jobs) == 1
		for _, j := range jobs {
			p.movies = append(p.movies, movieFor(root, isDir, j[0].dirs, j, alone))
		}
	}

	// Extras go with the movie when there's just one; with a show likewise
	// (moved after filing). Otherwise they stay, as videos nobody filed.
	if len(p.movies) == 1 && len(p.shows) == 0 {
		p.movies[0].extras = append(append(p.movies[0].extras, p.tvExtras...), extraFiles...)
		p.tvExtras = nil
	} else {
		for _, x := range p.tvExtras {
			p.videos = append(p.videos, x)
		}
		for _, x := range extraFiles {
			p.unknown = append(p.unknown, x)
		}
	}
	if len(p.movies) == 1 && len(p.shows) == 0 {
		for _, x := range p.movies[0].extras {
			if st, err := os.Stat(x); err == nil && st.IsDir() {
				p.videos = append(p.videos, x)
			}
		}
	}

	p.attachSubs(subs)
	return p
}

// forcedEpisode reads a number from a file known to be an episode (in a
// tv/ folder, or hinted): "Pilot 01", "Part 2", "03 Something".
func forcedEpisode(name string, folderSeason int) (int, []int, bool) {
	base := videoBase(name)
	if m := partNumRe.FindStringSubmatch(base); m != nil {
		return folderSeason, []int{Atoi(m[1])}, true
	}
	if m := reLeadNum.FindStringSubmatch(base); m != nil {
		return folderSeason, []int{Atoi(m[1])}, true
	}
	return 0, nil, false
}

// episodeShow works out which show an episode belongs to: from its file name
// when that starts with the show ("Severance.S01E01..."), otherwise from the
// nearest folder that isn't just a season ("Doctor Who (2005)/Season 1/01.mkv").
// The other one, if any, becomes an alternative search.
func episodeShow(name string, dirs []string) (Parsed, string, bool) {
	var fromFile Parsed
	if loc := showCut.FindStringIndex(cleanRunes(name)); loc != nil && loc[0] > 0 {
		fromFile = parseShowName(name)
	}
	var fromDir Parsed
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if seasonOnly(d) || isExtrasDir(d) || isSubsDir(d) {
			continue
		}
		if p := parseShowName(d); p.Title != "" {
			fromDir = p
			break
		}
	}
	switch {
	case fromFile.Title != "":
		alt := ""
		if fromDir.Title != "" && NormTitle(fromDir.Title) != NormTitle(fromFile.Title) {
			alt = fromDir.Title
		}
		if fromFile.Year == 0 && fromDir.Year != 0 && TitleSim(fromFile.Title, fromDir.Title) >= 0.8 {
			fromFile.Year = fromDir.Year
		}
		return fromFile, alt, true
	case fromDir.Title != "":
		return fromDir, "", true
	}
	return Parsed{}, "", false
}

var seasonOnlyRe = regexp.MustCompile(`(?i)^(?:season|series|staffel|saison|temporada|s)[ ._-]*\d{1,2}\s*$|^specials?$`)

func seasonOnly(name string) bool {
	return seasonOnlyRe.MatchString(strings.TrimSpace(cleanRunes(name)))
}

// movieFor names a movie: from its folder when that's the movie's own folder
// (a release folder, usually with the year), otherwise from the file.
func movieFor(root string, isDir bool, dirs []string, parts []movieCand, alone bool) *movieJob {
	var videos []string
	for _, f := range parts {
		videos = append(videos, f.path)
	}
	file := parseMovieName(filepath.Base(videos[0]))
	if len(parts) > 1 {
		file.Part = 0
	}
	label := filepath.Base(videos[0])
	if isDir {
		label = filepath.Join(filepath.Base(root), strings.TrimPrefix(videos[0], root+string(filepath.Separator)))
	}
	j := &movieJob{label: label, parsed: file, videos: videos}
	if alone && len(dirs) > 0 {
		d := dirs[len(dirs)-1]
		folder := parseMovieName(d)
		if folder.Title != "" && (folder.Year != 0 || TitleSim(folder.Title, file.Title) >= 0.6) {
			if folder.Year == 0 && TitleSim(folder.Title, file.Title) >= 0.8 {
				folder.Year = file.Year
			}
			if folder.Edition == "" {
				folder.Edition = file.Edition
			}
			if NormTitle(folder.Title) != NormTitle(file.Title) {
				j.alt = file.Title
			}
			j.parsed = folder
			if len(dirs) == 1 {
				j.label = d
			}
		} else if folder.Title != "" && NormTitle(folder.Title) != NormTitle(file.Title) {
			j.alt = folder.Title
		}
	}
	return j
}

// attachSubs gives subtitles from Subs/ folders (and loose ones not named
// after a video) to the video they belong to: the one whose name they
// carry, or whose name is their folder's ("Subs/Show.S01E01/2_English.srt"),
// or the only video there is.
func (p *itemPlan) attachSubs(subs []string) {
	var vids []string
	for _, f := range p.movies {
		vids = append(vids, f.videos...)
	}
	for _, sh := range p.shows {
		for _, e := range sh.eps {
			vids = append(vids, e.path)
		}
	}
	used := map[string]map[string]bool{}
	for _, sub := range subs {
		dir, name := filepath.Dir(sub), filepath.Base(sub)
		to, sidecar := "", false
		for _, v := range vids {
			vb := videoBase(v)
			if filepath.Dir(v) == dir && strings.HasPrefix(name, vb) {
				sidecar = true // named after its video, beside it: travels by itself
				break
			}
			if to == "" && (filepath.Base(dir) == vb || strings.HasPrefix(name, vb)) {
				to = v
			}
		}
		if sidecar {
			continue
		}
		if to == "" && len(vids) == 1 && len(p.movies) == 1 {
			to = vids[0]
		}
		if to != "" {
			if used[to] == nil {
				used[to] = map[string]bool{}
			}
			suffix := subSuffix(name)
			for n := 2; used[to][suffix]; n++ {
				ext := filepath.Ext(suffix)
				suffix = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(subSuffix(name), ext), n, ext)
			}
			used[to][suffix] = true
			p.subs[to] = append(p.subs[to], extraSub{sub, suffix})
		}
	}
}

var langCodes = map[string]string{
	"english": "en", "eng": "en", "en": "en", "french": "fr", "fre": "fr", "fra": "fr", "fr": "fr",
	"german": "de", "ger": "de", "deu": "de", "de": "de", "spanish": "es", "spa": "es", "es": "es",
	"italian": "it", "ita": "it", "it": "it", "dutch": "nl", "nld": "nl", "dut": "nl", "nl": "nl",
	"portuguese": "pt", "por": "pt", "pt": "pt", "brazilian": "pt-BR", "japanese": "ja", "jpn": "ja", "ja": "ja",
	"chinese": "zh", "chi": "zh", "zho": "zh", "zh": "zh", "korean": "ko", "kor": "ko", "ko": "ko",
	"swedish": "sv", "swe": "sv", "sv": "sv", "danish": "da", "dan": "da", "da": "da",
	"norwegian": "no", "nor": "no", "no": "no", "finnish": "fi", "fin": "fi", "fi": "fi",
	"polish": "pl", "pol": "pl", "pl": "pl", "russian": "ru", "rus": "ru", "ru": "ru",
	"arabic": "ar", "ara": "ar", "ar": "ar", "greek": "el", "gre": "el", "ell": "el", "el": "el",
	"hebrew": "he", "heb": "he", "he": "he", "turkish": "tr", "tur": "tr", "tr": "tr",
	"czech": "cs", "cze": "cs", "ces": "cs", "cs": "cs", "hungarian": "hu", "hun": "hu", "hu": "hu",
	"romanian": "ro", "rum": "ro", "ron": "ro", "ro": "ro", "hindi": "hi", "hin": "hi",
	"thai": "th", "tha": "th", "th": "th", "vietnamese": "vi", "vie": "vi", "vi": "vi",
	"indonesian": "id", "ind": "id", "ukrainian": "uk", "ukr": "uk",
}

var subTokens = regexp.MustCompile(`[^\pL\pN]+`)

// subSuffix turns a subtitle's own name ("2_English.srt",
// "English (SDH).srt", "eng.forced.srt") into a sidecar suffix (".en.sdh.srt").
func subSuffix(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	base := subLeadNum.ReplaceAllString(strings.TrimSuffix(name, filepath.Ext(name)), "")
	lang, forced, sdh := "", false, false
	for _, t := range subTokens.Split(strings.ToLower(base), -1) {
		switch {
		case t == "forced" || t == "foreign":
			forced = true
		case t == "sdh" || t == "cc" || t == "hi":
			sdh = true // ("hi" is also Hindi's code; for subtitles it's nearly always "hearing impaired")
		case lang == "" && langCodes[t] != "":
			lang = langCodes[t]
		}
	}
	out := ""
	if lang != "" {
		out += "." + lang
	}
	if forced {
		out += ".forced"
	}
	if sdh {
		out += ".sdh"
	}
	return out + ext
}

func hasVideos(dir string) bool {
	found := false
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !skipName(info.Name()) && isVideo(p) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// macJunk says whether a file is one a Mac leaves behind (Finder's
// .DS_Store, AppleDouble ._ files): a folder holding only these is empty.
func macJunk(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

func emptyTree(dir string) bool {
	empty := true
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !macJunk(info.Name()) {
			empty = false
			return filepath.SkipAll
		}
		return nil
	})
	return empty
}

// --- hints ---

// readHint reads an item's .mvd: inside a folder, or "Name.mvd" beside a file.
func readHint(path string, isDir bool) hint {
	p := filepath.Join(path, ".mvd")
	if !isDir {
		p = strings.TrimSuffix(path, filepath.Ext(path)) + ".mvd"
	}
	kv, err := readKV(p)
	if err != nil {
		return hint{}
	}
	h := hint{path: p, tmdb: Atoi(kv["tmdb"]), order: kv["order"]}
	switch strings.ToLower(kv["kind"]) {
	case "movie", "movies":
		h.kind = "movie"
	case "tv", "show", "series":
		h.kind = "tv"
	}
	return h
}
