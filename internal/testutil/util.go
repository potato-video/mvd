package testutil

import (
	"os"
	"strconv"
	"strings"
	"unicode"
)

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	return atoi(date[:4])
}

// titleSim is a rough copy of the library's title matching: good enough for
// a fake TMDB's search.
func titleSim(a, b string) float64 {
	a, b = norm(a), norm(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	return max(levRatio(a, b), tokenDice(a, b))
}

func norm(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	f := strings.Fields(s)
	if len(f) > 1 && (f[0] == "the" || f[0] == "a" || f[0] == "an") {
		f = f[1:]
	}
	return strings.Join(f, " ")
}

func levRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}

func tokenDice(a, b string) float64 {
	ta, tb := strings.Fields(a), strings.Fields(b)
	set := map[string]int{}
	for _, t := range ta {
		set[t]++
	}
	common := 0
	for _, t := range tb {
		if set[t] > 0 {
			set[t]--
			common++
		}
	}
	return 2 * float64(common) / float64(len(ta)+len(tb))
}
