// Package lang turns the ways a language is written (ISO 639-1 and 639-2
// codes, BCP 47 tags, English names, TMDB's codes) into one code, and
// names it in English.
package lang

import (
	"strings"
	"sync"
)

// Silent is the code for no language: BCP 47's "no linguistic content",
// TMDB's "xx".
const Silent = "zxx"

// Code is s's language as an ISO 639-1 code ("da"), or the BCP 47 code
// where there isn't one ("zxx", "yue"); "" if it isn't one it knows.
// "fr-CA" is "fr", "dan" and "Danish" are "da", TMDB's "xx" is "zxx" and
// its "cn" is "yue".
func Code(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	return codes()[s]
}

// Name is a code's English name, "Silent" for zxx; "" if it isn't one.
func Name(code string) string {
	if code == Silent {
		return "Silent"
	}
	return names()[code]
}

var codes = sync.OnceValue(func() map[string]string {
	m := map[string]string{"xx": Silent, Silent: Silent, "no language": Silent, "cn": "yue"}
	for _, l := range languages {
		for _, k := range []string{l.code, l.t, l.b, strings.ToLower(l.name)} {
			if k != "" {
				m[k] = l.code
			}
		}
	}
	return m
})

var names = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	for _, l := range languages {
		m[l.code] = l.name
	}
	return m
})
