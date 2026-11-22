package library

import (
	"unicode"
	"unicode/utf8"
)

// Search: one search for every client (the API, the website, mvd search).
// Every word typed must start a word of a title, a movie's collection or a
// director's name (or carry on where the word typed before it stopped, so
// "god fa" finds "Godfather"), or be the year; case, accents and
// punctuation don't count. It's a loop over the index, which is in memory already.
//
// On the router a request per keystroke pause mustn't make garbage per
// title: the query is folded once, and each title is folded into one buffer
// that's reused for the whole search.

// SearchLimit is how many of each kind a search gives at most, so a
// one-letter query stays small.
const SearchLimit = 40

// SearchResults is what a search found, best first within each kind.
type SearchResults struct {
	Movies   []*Movie
	Shows    []*Show
	Episodes []*Episode
}

// Ranks, best first. Ties keep the library's order.
const (
	rankExact  = iota // the whole title
	rankPrefix        // the title starts with what's typed
	rankWords         // every word starts a word of the title
	rankOther         // a match elsewhere: collection, director, year, an episode's show
	ranks
)

// Search finds the movies, shows and episodes matching q, at most limit of
// each. allow says whether something with that certification may be seen
// (an episode goes by its show's); nil allows everything.
func (ix *Index) Search(q string, limit int, allow func(rating string) bool) SearchResults {
	var res SearchResults
	s := newSearch(q)
	if len(s.words) == 0 || limit <= 0 {
		return res
	}
	var movies bucket[*Movie]
	var shows bucket[*Show]
	var eps bucket[*Episode]
	for _, m := range ix.Movies {
		if allow != nil && !allow(m.Rating) {
			continue
		}
		mask, rank := s.title(m.Title)
		if mask != s.all {
			rank = rankOther
			mask |= s.year(m.Year) | s.starts(m.Set)
			for _, d := range m.Directors {
				if mask == s.all {
					break
				}
				mask |= s.starts(d)
			}
		}
		if mask == s.all {
			movies.add(rank, m, limit)
		}
	}
	for _, sh := range ix.Shows {
		if allow != nil && !allow(sh.Rating) {
			continue
		}
		showMask, rank := s.title(sh.Title)
		if showMask == s.all {
			shows.add(rank, sh, limit)
		} else if showMask|s.year(sh.Year) == s.all {
			shows.add(rankOther, sh, limit)
		}
		// An episode matches on its show's title too ("sopranos pine"),
		// below those that match on their own.
		for _, se := range sh.Seasons {
			for _, e := range se.Episodes {
				mask, rank := s.title(e.Title)
				if mask == s.all {
					eps.add(rank, e, limit)
				} else if mask|showMask == s.all {
					eps.add(rankOther, e, limit)
				}
			}
		}
	}
	res.Movies = movies.best(limit)
	res.Shows = shows.best(limit)
	res.Episodes = eps.best(limit)
	return res
}

// bucket keeps up to limit matches at each rank, in the order they came,
// so the best limit can be read off without sorting everything that
// matched.
type bucket[T any] [ranks][]T

func (b *bucket[T]) add(rank int, v T, limit int) {
	if len(b[rank]) < limit {
		b[rank] = append(b[rank], v)
	}
}

func (b *bucket[T]) best(limit int) []T {
	var out []T
	for _, r := range b {
		if n := limit - len(out); len(r) > n {
			r = r[:n]
		}
		out = append(out, r...)
	}
	return out
}

// search is one query, folded, and the buffer titles are folded into.
type search struct {
	text  []rune   // the query folded: words with one space between
	words [][]rune // each word, a slice of text
	years []int    // a word that could be a year, as a number; else 0
	all   uint32   // a bit per word
	buf   []rune
}

const maxSearchWords = 8 // more words than this are ignored

func newSearch(q string) *search {
	s := &search{text: appendFolded(nil, q)}
	start := 0
	for i := 0; i <= len(s.text); i++ {
		if i < len(s.text) && s.text[i] != ' ' {
			continue
		}
		if i > start && len(s.words) < maxSearchWords {
			w := s.text[start:i]
			s.words = append(s.words, w)
			s.years = append(s.years, yearOf(w))
		}
		start = i + 1
	}
	s.all = 1<<len(s.words) - 1
	return s
}

// yearOf is a four-digit word as a number, 0 if it isn't one.
func yearOf(w []rune) int {
	if len(w) != 4 {
		return 0
	}
	n := 0
	for _, r := range w {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// year is the words that are this year.
func (s *search) year(y int) (mask uint32) {
	if y == 0 {
		return 0
	}
	for i, n := range s.years {
		if n == y {
			mask |= 1 << i
		}
	}
	return mask
}

// starts is the query's words that start a word of text.
func (s *search) starts(text string) uint32 {
	if text == "" {
		return 0
	}
	s.buf = appendFolded(s.buf[:0], text)
	return s.startsFolded(s.buf)
}

func (s *search) startsFolded(t []rune) (mask uint32) {
	for i := range t {
		if i > 0 && t[i-1] != ' ' {
			continue
		}
		// From each word start, a run of the query's words one after
		// another: "god" alone, or "god" then "fa" inside "godfather".
		for j := range s.words {
			at := i
			for k := j; k < len(s.words) && hasPrefix(t[at:], s.words[k]); k++ {
				mask |= 1 << k
				at += len(s.words[k])
			}
		}
		if mask == s.all {
			break
		}
	}
	return mask
}

// title is the query's words that start a word of a title and, when
// that's all of them, how good a match it is. A leading article doesn't
// count: "godfather" is the whole of "The Godfather".
func (s *search) title(title string) (mask uint32, rank int) {
	mask = s.starts(title)
	if mask != s.all {
		return mask, rankOther
	}
	t := s.buf
	for range 2 {
		if hasPrefix(t, s.text) {
			if len(t) == len(s.text) {
				return mask, rankExact
			}
			rank = rankPrefix
		}
		rest := afterArticle(t)
		if len(rest) == len(t) {
			break
		}
		t = rest
	}
	if rank == rankPrefix {
		return mask, rankPrefix
	}
	return mask, rankWords
}

// afterArticle is a folded title without a leading "the", "a" or "an",
// when there's more after it.
func afterArticle(t []rune) []rune {
	for _, a := range [...]string{"the ", "an ", "a "} {
		if len(t) > len(a) && string(t[:len(a)]) == a { // no allocation: compared in place
			return t[len(a):]
		}
	}
	return t
}

func hasPrefix(t, w []rune) bool {
	if len(w) > len(t) {
		return false
	}
	for i, r := range w {
		if t[i] != r {
			return false
		}
	}
	return true
}

// appendFolded adds s to dst folded for matching: lower case, accents
// taken off (é → e, ø → o, ß → ss), apostrophes dropped ("Ocean's" is
// "oceans"), "&" as "and", and anything else that isn't a letter or digit
// as one space between words.
func appendFolded(dst []rune, s string) []rune {
	n := len(dst)
	gap := false // a word break is owed before the next letter
	for i := 0; i < len(s); {
		r, r2 := rune(s[i]), rune(0)
		if r < utf8.RuneSelf {
			i++
			switch {
			case 'a' <= r && r <= 'z', '0' <= r && r <= '9':
			case 'A' <= r && r <= 'Z':
				r += 'a' - 'A'
			case r == '\'':
				continue
			case r == '&':
				if len(dst) > n {
					dst = append(dst, ' ')
				}
				dst = append(dst, 'a', 'n', 'd')
				gap = true
				continue
			default:
				gap = true
				continue
			}
		} else {
			var size int
			r, size = utf8.DecodeRuneInString(s[i:])
			i += size
			switch {
			case r == '’' || r == '‘' || r == 'ʼ' || unicode.Is(unicode.Mn, r):
				// Apostrophes, and combining accents (names from a Mac
				// are often decomposed: e, then the accent).
				continue
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				r, r2 = baseLetter(unicode.ToLower(r))
			default:
				gap = true
				continue
			}
		}
		if gap && len(dst) > n {
			dst = append(dst, ' ')
		}
		gap = false
		dst = append(dst, r)
		if r2 != 0 {
			dst = append(dst, r2)
		}
	}
	return dst
}

// latinA is the base letter of each of U+0100 to U+017F (Latin Extended-A).
const latinA = "aaaaaa" + "cccccccc" + "dddd" + "eeeeeeeeee" + "gggggggg" + "hhhh" +
	"iiiiiiiiii" + "ii" + "jj" + "kkk" + "llllllllll" + "nnnnnnnnn" + "oooooo" + "oo" +
	"rrrrrr" + "ssssssss" + "tttttt" + "uuuuuuuuuuuu" + "ww" + "yyy" + "zzzzzz" + "s"

// baseLetter takes a lower-case letter of Latin-1 or Latin Extended-A to
// its unaccented letter, or two (ß → ss, æ → ae). Other letters are
// themselves. By hand, since the standard library has no normalization.
func baseLetter(r rune) (rune, rune) {
	switch {
	case r < 0xDF:
		return r, 0
	case r <= 0xFF:
		switch r {
		case 0xDF:
			return 's', 's'
		case 0xE6:
			return 'a', 'e'
		case 0xFE:
			return 't', 'h'
		}
		const latin1 = "_aaaaaa_ceeeeiiiidnooooo_ouuuuy_y" // U+00DF to U+00FF
		return rune(latin1[r-0xDF]), 0
	case r <= 0x17F:
		switch r {
		case 0x133:
			return 'i', 'j'
		case 0x153:
			return 'o', 'e'
		}
		return rune(latinA[r-0x100]), 0
	}
	return r, 0
}
