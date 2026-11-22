package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true,
	".wmv": true, ".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
	".webm": true, ".ogv": true, ".divx": true, ".vob": true, ".iso": true,
}

func isVideoExt(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// IsVideoName says whether a file name is a video's, by its extension.
func IsVideoName(name string) bool { return isVideoExt(name) }

// UploadExts are what can be uploaded besides videos: subtitles, and the
// .nfo that can say which movie it is.
var UploadExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true, ".sup": true,
	".nfo": true,
}

// Uploadable says whether a file can be uploaded into the import folder:
// a video, subtitles, an .nfo, or (with the unpack feature) a .zip or
// .rar, any volume of a set, which the importer unpacks; by its extension.
func Uploadable(name string) bool {
	return isVideoExt(name) || UploadExts[strings.ToLower(filepath.Ext(name))] || isArchive(name)
}

// UploadKinds says what can be uploaded, for messages: "videos, subtitles,
// .nfo files and .zip or .rar archives".
func UploadKinds() string {
	if unpackBuilt {
		return "videos, subtitles, .nfo files and .zip or .rar archives"
	}
	return "videos, subtitles and .nfo files"
}

// sniffVideo identifies a video container from its first bytes, for files
// that have lost their extension. Returns "" if it doesn't look like video.
func sniffVideo(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 200)
	n, _ := f.Read(b)
	b = b[:n]
	switch {
	case n >= 4 && string(b[:4]) == "\x1a\x45\xdf\xa3":
		if strings.Contains(string(b), "webm") {
			return ".webm"
		}
		return ".mkv"
	case n >= 12 && string(b[4:8]) == "ftyp":
		if string(b[8:12]) == "qt  " {
			return ".mov"
		}
		return ".mp4"
	case n >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "AVI ":
		return ".avi"
	case n >= 4 && string(b[:4]) == "\x30\x26\xb2\x75":
		return ".wmv"
	case n >= 4 && string(b[:4]) == "\x00\x00\x01\xba":
		return ".mpg"
	case n >= 189 && b[0] == 0x47 && b[188] == 0x47:
		return ".ts"
	}
	return ""
}

// cleanRunes undoes what macOS and friends do to names on SMB shares:
// private-use stand-ins for illegal characters, non-breaking spaces and
// other invisible oddities all become plain spaces.
func cleanRunes(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xF000 && r <= 0xF0FF: // Services-for-Mac character mapping
			return ' '
		case r == 0x00A0 || r == 0x2007 || r == 0x202F: // non-breaking spaces
			return ' '
		case r == 0x200B || r == 0xFEFF: // zero-width
			return -1
		case r == '’' || r == '‘':
			return '\''
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

var (
	resRe   = regexp.MustCompile(`^\d{3,4}[pi]$`)
	yearRe  = regexp.MustCompile(`^(18|19|20)\d{2}$`)
	partRe  = regexp.MustCompile(`(?i)\b(?:cd|dis[ck]|part|pt)[ ._-]*([1-9])\b`)
	partTok = regexp.MustCompile(`(?i)^(?:cd|dis[ck]|pt)[1-9]$`)
	leadTag = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)
)

var junkWords = map[string]bool{
	// Only words that never turn up in real titles ("web", "hd", "dvd" do).
	"4k": true, "uhd": true,
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true,
	"webrip": true, "web-dl": true, "webdl": true, "hdtv": true, "hdrip": true,
	"dvdrip": true, "dvdscr": true, "hdcam": true, "ntsc": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true,
	"xvid": true, "divx": true, "av1": true, "10bit": true, "8bit": true,
	"hdr": true, "hdr10": true, "sdr": true,
	"aac": true, "ac3": true, "eac3": true, "dts": true, "truehd": true, "atmos": true, "flac": true, "mp3": true,
	"proper": true, "repack": true, "internal": true, "limited": true, "multi": true,
	"subbed": true, "dubbed": true, "remastered": true,
	"extended": true, "unrated": true, "uncut": true, "theatrical": true,
	"directors": true, "director's": true, "imax": true, "criterion": true,
}

func isJunk(tok string) bool {
	t := strings.ToLower(tok)
	if junkWords[t] || resRe.MatchString(t) || partTok.MatchString(t) {
		return true
	}
	if i := strings.IndexByte(t, '-'); i > 0 { // "x264-GROUP"
		return isJunk(t[:i])
	}
	return false
}

var editions = []struct{ match, name string }{
	{"director's cut", "Director's Cut"},
	{"directors cut", "Director's Cut"},
	{"extended cut", "Extended Cut"},
	{"extended edition", "Extended Edition"},
	{"theatrical cut", "Theatrical Cut"},
	{"final cut", "Final Cut"},
	{"special edition", "Special Edition"},
	{"ultimate edition", "Ultimate Edition"},
	{"unrated", "Unrated"},
	{"uncut", "Uncut"},
	{"extended", "Extended"},
	{"theatrical", "Theatrical"},
	{"imax", "IMAX"},
}

// Parsed is what can be read out of a movie's folder or file name.
type Parsed struct {
	Title   string
	Year    int
	Edition string // "Director's Cut"
	Part    int    // multi-part files: cd1, part2...
	Country string // TV: "The Office (US)"
}

// parseMovieName pulls a title and year out of names like
// "The.Thing.1982.1080p.BluRay.x264-GROUP" or "Blade Runner 2049 (2017)".
func parseMovieName(name string) Parsed {
	s := cleanRunes(name)
	if isVideoExt(s) {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	for leadTag.MatchString(s) {
		s = leadTag.ReplaceAllString(s, "")
	}
	s = strings.ReplaceAll(s, "_", " ")
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = strings.ReplaceAll(s, ".", " ")
	}
	s = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ").Replace(s)
	toks := strings.Fields(s)

	// Everything from the first junk token on is release clutter.
	junkAt := len(toks)
	for i := 1; i < len(toks); i++ {
		if isJunk(toks[i]) {
			junkAt = i
			break
		}
	}

	// The year is the last year-like token before the clutter, and never the
	// first token ("1917", "2001: A Space Odyssey"). If a "junk" word sneaks
	// into a real title, fall back to the first year after it.
	var p Parsed
	end := junkAt
	isYear := func(i int) bool {
		return yearRe.MatchString(toks[i]) && Atoi(toks[i]) <= time.Now().Year()+1
	}
	for i := junkAt - 1; i >= 1; i-- {
		if isYear(i) {
			p.Year, end = Atoi(toks[i]), i
			break
		}
	}
	if p.Year == 0 {
		for i := junkAt; i < len(toks); i++ {
			if isYear(i) {
				p.Year, end = Atoi(toks[i]), i
				break
			}
		}
	}

	title := strings.Join(toks[:end], " ")
	p.Title = strings.TrimSpace(strings.TrimRight(title, " -.,:;"))

	rest := strings.ToLower(strings.Join(toks[end:], " "))
	for _, e := range editions {
		if strings.Contains(rest, e.match) {
			p.Edition = e.name
			break
		}
	}
	if m := partRe.FindStringSubmatch(rest); m != nil {
		p.Part = Atoi(m[1])
	}
	return p
}

// SafeName makes a title fit for an exFAT/FAT/SMB filename, and one the
// scans don't skip (skipName): ".45" is filed as "45".
func SafeName(s string) string {
	s = cleanRunes(s)
	s = strings.NewReplacer(
		": ", " - ",
		":", "-",
		"/", "-",
		"\\", "-",
		"|", "-",
		"?", "",
		"*", "",
		"\"", "'",
		"<", "",
		">", "",
	).Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimLeft(strings.TrimRight(s, " ."), " ._@")
}

// Atoi is a string of digits as a number, or 0 if it's anything else or
// too big for an int.
func Atoi(s string) int {
	const most = int(^uint(0) >> 1)
	n := 0
	for _, c := range s {
		d := int(c - '0')
		if c < '0' || c > '9' || n > (most-d)/10 {
			return 0
		}
		n = n*10 + d
	}
	return n
}
