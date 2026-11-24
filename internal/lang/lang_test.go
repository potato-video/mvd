package lang

import "testing"

func TestCode(t *testing.T) {
	for in, want := range map[string]string{
		"da":          "da",
		"DA":          "da",
		" da ":        "da",
		"dan":         "da", // ISO 639-2/T
		"ger":         "de", // ISO 639-2/B
		"deu":         "de",
		"fr-CA":       "fr", // BCP 47: the language part
		"zh_Hant_TW":  "zh",
		"French":      "fr",
		"greek":       "el",
		"xx":          "zxx", // TMDB's "No Language"
		"zxx":         "zxx",
		"No Language": "zxx",
		"cn":          "yue", // TMDB's Cantonese
		"yue":         "yue",
		"Cantonese":   "yue",
		"":            "",
		"qq":          "",
		"Klingon":     "",
	} {
		if got := Code(in); got != want {
			t.Errorf("Code(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestName(t *testing.T) {
	for in, want := range map[string]string{
		"da":  "Danish",
		"el":  "Greek",
		"zxx": "Silent",
		"yue": "Cantonese",
		"qq":  "",
		"":    "",
	} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}
