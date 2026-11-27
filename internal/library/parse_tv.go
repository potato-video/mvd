package library

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// Where a show's name ends in a scene-style name: S01E02, S01, 1x02,
	// "Season 2", "Complete".
	showCut    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,2}(?:[ ._-]?e\d{1,3})?|\d{1,2}x\d{2,3}|(?:season|series)[ ._-]?\d{1,2}|complete)(?:[^a-z0-9]|$)`)
	countryTag = regexp.MustCompile(`[ ._(\[]+(US|UK|AU|NZ|CA|IE)[)\]]?\s*$`)
	yearTail   = regexp.MustCompile(`[ ._]*[(\[]?(?:19|20)\d{2}[)\]]?\s*$`)

	reSE       = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]?e(\d{1,3})`)
	reMoreE    = regexp.MustCompile(`(?i)^([ ._]*(-)?[ ._]*)e(\d{1,3})`)
	reMoreDash = regexp.MustCompile(`^-(\d{1,3})(?:[^0-9]|$)`)
	reX        = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)
	reMoreX    = regexp.MustCompile(`(?i)^[ ._]*-[ ._]*(?:\d{1,2}x)?(\d{2,3})(?:[^0-9]|$)`)
	reSeasonEp = regexp.MustCompile(`(?i)season[ ._-]?(\d{1,2})[ ._-]*(?:episode|ep)[ ._-]?(\d{1,3})`)
	reEpOnly   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[ ._-]?(\d{1,3})(?:[^0-9]|$)`)
	reLeadNum  = regexp.MustCompile(`^(\d{1,3})(?:[ ._-]|$)`)

	reSeasonDirStart = regexp.MustCompile(`(?i)^(?:season|series|staffel|saison|temporada|s)[ ._-]*(\d{1,2})(?:[^0-9]|$)`)
	reSeasonDirAny   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:season|series)[ ._-]*(\d{1,2})(?:[^0-9]|$)`)
	reSeasonDirTag   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})(?:[^0-9e]|$)`)
)

// parseShowName reads a show's name (and year, and US/UK tag) from a show
// folder or an episode file name.
func parseShowName(name string) Parsed {
	s := cleanRunes(name)
	if isVideoExt(s) {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	if loc := showCut.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s = s[:loc[0]]
	}
	// "The Office (US)" or "The Office (US) (2005)"
	country, tail := "", ""
	if loc := yearTail.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s, tail = s[:loc[0]], s[loc[0]:]
	}
	if m := countryTag.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
		country = s[m[2]:m[3]]
		s = s[:m[0]]
	}
	s += tail
	p := parseMovieName(s)
	p.Country = country
	p.Edition, p.Part = "", 0
	return p
}

// parseEpisode finds the season and episode number(s) in a file name.
// folderSeason is the season implied by the folders above it, or -1.
// A returned season of -1 means "episode number known, season not".
func parseEpisode(name string, folderSeason int) (season int, eps []int, ok bool) {
	s := cleanRunes(name)
	if isVideoExt(s) {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}

	if m := reSE.FindStringSubmatchIndex(s); m != nil {
		season = Atoi(s[m[2]:m[3]])
		eps = []int{Atoi(s[m[4]:m[5]])}
		return season, moreEpisodes(s[m[1]:], eps), true
	}
	if m := reX.FindStringSubmatchIndex(s); m != nil {
		season = Atoi(s[m[2]:m[3]])
		eps = []int{Atoi(s[m[4]:m[5]])}
		if mm := reMoreX.FindStringSubmatch(s[m[5]:]); mm != nil {
			eps = extendRange(eps, Atoi(mm[1]))
		}
		return season, eps, true
	}
	if m := reSeasonEp.FindStringSubmatch(s); m != nil {
		return Atoi(m[1]), []int{Atoi(m[2])}, true
	}
	if m := reEpOnly.FindStringSubmatch(s); m != nil {
		return folderSeason, []int{Atoi(m[1])}, true
	}
	if folderSeason >= 0 { // "02 - Title.mkv" inside "Season 1"
		if m := reLeadNum.FindStringSubmatch(s); m != nil {
			return folderSeason, []int{Atoi(m[1])}, true
		}
	}
	return 0, nil, false
}

// moreEpisodes reads what follows S01E01: "E02E03" lists episodes, while
// "-E03" and "-03" end a range.
func moreEpisodes(rest string, eps []int) []int {
	for {
		m := reMoreE.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		n := Atoi(m[3])
		if m[2] == "-" {
			eps = extendRange(eps, n)
		} else if last := eps[len(eps)-1]; n > last && n-last <= 10 {
			eps = append(eps, n)
		} else {
			return eps
		}
		rest = rest[len(m[0]):]
	}
	if m := reMoreDash.FindStringSubmatch(rest); m != nil {
		eps = extendRange(eps, Atoi(m[1]))
	}
	return eps
}

// extendRange adds last+1..n, ignoring nonsense like "-720p".
func extendRange(eps []int, n int) []int {
	last := eps[len(eps)-1]
	if n <= last || n-last > 10 {
		return eps
	}
	for e := last + 1; e <= n; e++ {
		eps = append(eps, e)
	}
	return eps
}

// folderSeason reads a season number from a folder name: "Season 1",
// "Series 02", "S01", "Show.S02.1080p", "Specials".
func folderSeason(name string) int {
	s := cleanRunes(name)
	if strings.EqualFold(s, "specials") || strings.EqualFold(s, "special") {
		return 0
	}
	for _, re := range []*regexp.Regexp{reSeasonDirStart, reSeasonDirAny, reSeasonDirTag} {
		if m := re.FindStringSubmatch(s); m != nil {
			return Atoi(m[1])
		}
	}
	return -1
}

// Folders of bonus material that aren't episodes (Jellyfin's names, mostly).
var extrasDirs = map[string]bool{
	"extras": true, "extra": true, "featurettes": true, "behind the scenes": true,
	"deleted scenes": true, "interviews": true, "scenes": true, "samples": true,
	"sample": true, "shorts": true, "trailers": true, "other": true, "clips": true,
}

func isExtrasDir(name string) bool {
	return extrasDirs[strings.ToLower(strings.TrimSpace(cleanRunes(name)))]
}

// episodeTitle reads the episode's own title from a file name: what follows
// its number, up to the first release tag ("Show.S02E07.The.Ghost.of.
// Christmas.720p.WEB-DL" has "The Ghost of Christmas"). "" if there's none.
func episodeTitle(name string) string {
	s := cleanRunes(name)
	if isVideoExt(s) {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	end := -1
	for _, re := range []*regexp.Regexp{reSE, reX, reSeasonEp} {
		if m := re.FindStringIndex(s); m != nil {
			end = m[1]
			break
		}
	}
	if end < 0 {
		if m := reLeadNum.FindStringIndex(s); m != nil {
			end = m[1]
		}
	}
	if end < 0 {
		return ""
	}
	toks := strings.FieldsFunc(s[end:], func(r rune) bool { return r == ' ' || r == '.' || r == '_' })
	// More episode numbers first ("E02", "-E03", "-03"), and dashes.
	for len(toks) > 0 && (toks[0] == "-" || moreEpTok.MatchString(toks[0])) {
		toks = toks[1:]
	}
	var title []string
	for _, t := range toks {
		if isJunk(t) || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "(") {
			break
		}
		title = append(title, t)
	}
	for len(title) > 0 && title[len(title)-1] == "-" {
		title = title[:len(title)-1]
	}
	return strings.Join(title, " ")
}

var moreEpTok = regexp.MustCompile(`(?i)^-?(?:e\d{1,3})+$|^-\d{1,3}$`)
