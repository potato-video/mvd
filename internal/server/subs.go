package server

import (
	"bytes"
	"unicode/utf8"
)

// srtToVTT converts SubRip to WebVTT for browsers: a header, and full stops
// rather than commas in the timestamps. Old SRTs are often Windows-1252
// rather than UTF-8, so those are converted too.
func srtToVTT(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		b = cp1252ToUTF8(b)
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	out := []byte("WEBVTT\n\n")
	for _, line := range bytes.Split(b, []byte("\n")) {
		if bytes.Contains(line, []byte("-->")) {
			line = bytes.ReplaceAll(line, []byte(","), []byte("."))
		}
		out = append(append(out, line...), '\n')
	}
	return out
}

// Windows-1252's 0x80-0x9F; everything else maps straight to Latin-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func cp1252ToUTF8(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	for _, c := range b {
		r := rune(c)
		if c >= 0x80 && c <= 0x9F {
			r = cp1252[c-0x80]
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}
