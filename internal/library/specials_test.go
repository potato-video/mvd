package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvd/internal/testutil"
)

func TestEpisodeTitle(t *testing.T) {
	for name, want := range map[string]string{
		"Ghosts UK 2019 S02E07 The Ghost of Christmas 720p WEB-DL HEVC x265 BONE.mkv": "The Ghost of Christmas",
		"Ghosts.S03E07.He.Came.1080p.WEB.H264-GRP.mkv":                                "He Came",
		"Ghosts - S04E07 - It's Behind You.mkv":                                       "It's Behind You",
		"Show.S01E01E02.Part.One.and.Two.HDTV.mkv":                                    "Part One and Two",
		"Show 1x07 A Title [720p].mkv":                                                "A Title",
		"Show.S02E05.720p.WEB-DL.mkv":                                                 "",
		"Show.S02E05.mkv":                                                             "",
		"02 - Rose.mkv":                                                               "Rose",
	} {
		if got := episodeTitle(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// A release that numbers the specials as an extra episode of a season:
// TMDB has them in season 0, which a title that matches one, and only one,
// finds. The rest are left, each with why.
func TestImportFindsSpecialsByTitle(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3"
	item := "Ghosts UK 2019 S01-S03 Complete 720p WEB-DL x265 BONE"
	in := func(p string) string {
		testutil.Write(t, filepath.Join(l.Import, item, p), mkv)
		return filepath.Join(l.Import, item, p)
	}
	in("S01/Ghosts UK 2019 S01E01 Who Do You Think You Are 720p WEB-DL x265 BONE.mkv")
	in("S02/Ghosts UK 2019 S02E01 The Grey Lady 720p WEB-DL x265 BONE.mkv")
	in("S02/Ghosts UK 2019 S02E03 The Ghost of Christmas 720p WEB-DL x265 BONE.mkv") // TMDB has no S02E03
	in("S03/Ghosts UK 2019 S03E01 He Came 720p WEB-DL x265 BONE.mkv")                // nor a season 3
	bloopers := in("S02/Ghosts UK 2019 S02E04 Bloopers 720p WEB-DL x265 BONE.mkv")   // two specials it might be
	untitled := in("S02/Ghosts UK 2019 S02E05 720p WEB-DL x265 BONE.mkv")            // no title to go on

	s := newTestImporter(t, l, srv, Options{}, "")
	s.Run()

	show := filepath.Join(l.TV, "Ghosts (2019)")
	for _, p := range []string{
		"Season 01/Ghosts (2019) - S01E01 - Who Do You Think You Are.mkv",
		"Season 02/Ghosts (2019) - S02E01 - The Grey Lady.mkv",
		"Season 00/Ghosts (2019) - S00E01 - The Ghost of Christmas.mkv",
		"Season 00/Ghosts (2019) - S00E02 - He Came!.mkv",
	} {
		if !Exists(filepath.Join(show, p)) {
			t.Errorf("not filed: %s\n%s", p, strings.Join(testutil.Tree(l.TV), "\n"))
		}
	}

	r := s.Results()[0]
	if r.State != ItemLeft || len(r.Units) != 1 || r.Units[0].Episodes != 4 || len(r.Unfiled) != 2 {
		t.Fatalf("result: %+v", r)
	}
	if why := r.Why[bloopers]; !strings.Contains(why, "TMDB has no S02E04") {
		t.Errorf("bloopers: %q", why)
	}
	if why := r.Why[untitled]; !strings.Contains(why, "TMDB has no S02E05") {
		t.Errorf("untitled: %q", why)
	}

	// Answers, one per file, in the item's hint: the next import files them.
	dir := filepath.Join(l.Import, item)
	if err := WriteEpisodeHint(dir, bloopers, 17174, 0, 4); err != nil {
		t.Fatal(err)
	}
	if err := WriteEpisodeHint(dir, untitled, 17174, 0, 3); err != nil {
		t.Fatal(err)
	}
	h := readHint(dir, true)
	if h.kind != "tv" || h.tmdb != 17174 || len(h.episodes) != 2 ||
		h.episodes["S02/Ghosts UK 2019 S02E04 Bloopers 720p WEB-DL x265 BONE.mkv"] != [2]int{0, 4} {
		t.Fatalf("hint: %+v", h)
	}
	s = newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	for _, p := range []string{
		"Season 00/Ghosts (2019) - S00E04 - Series 2 Bloopers.mkv",
		"Season 00/Ghosts (2019) - S00E03 - Series 1 Bloopers.mkv",
	} {
		if !Exists(filepath.Join(show, p)) {
			t.Errorf("not filed: %s\n%s", p, strings.Join(testutil.Tree(l.TV), "\n"))
		}
	}
	if r := s.Results()[0]; r.State != ItemFiled {
		t.Errorf("answered: %+v", r)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("the item's still there:\n%s", strings.Join(testutil.Tree(l.Import), "\n"))
	}
}

func TestWriteEpisodeHintKeepsWhatsThere(t *testing.T) {
	dir := t.TempDir()
	item := filepath.Join(dir, "BTAS")
	testutil.Write(t, filepath.Join(item, "a = b.mkv"), "x")
	testutil.Write(t, filepath.Join(item, ".mvd"), "kind = \"tv\"\ntmdb = 2098\norder = \"dvd\"\n")
	if err := WriteEpisodeHint(item, filepath.Join(item, "a = b.mkv"), 2098, 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := WriteEpisodeHint(item, filepath.Join(item, "a = b.mkv"), 2098, 1, 4); err != nil { // a change of mind
		t.Fatal(err)
	}
	h := readHint(item, true)
	if h.tmdb != 2098 || h.order != "dvd" || len(h.episodes) != 1 || h.episodes["a = b.mkv"] != [2]int{1, 4} {
		t.Errorf("%+v", h)
	}
	// Another show's answer starts over: those episodes were another show's.
	if err := WriteEpisodeHint(item, filepath.Join(item, "a = b.mkv"), 17174, 0, 1); err != nil {
		t.Fatal(err)
	}
	if h := readHint(item, true); h.tmdb != 17174 || h.order != "" || h.episodes["a = b.mkv"] != [2]int{0, 1} {
		t.Errorf("another show: %+v", h)
	}
}

// At a terminal, a video whose number won't do is asked about: one of the
// closest by title, or an episode typed, checked against TMDB.
func TestImportAsksWhichEpisode(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3"
	item := "Ghosts.2019.S02.720p.WEB-DL"
	testutil.Write(t, filepath.Join(l.Import, item, "Ghosts.2019.S02E04.Bloopers.720p.WEB-DL.mkv"), mkv)
	testutil.Write(t, filepath.Join(l.Import, item, "Ghosts.2019.S02E05.720p.WEB-DL.mkv"), mkv)

	s := newTestImporter(t, l, srv, Options{}, "")
	var out strings.Builder
	// Bloopers: the second closest (Series 2's). The untitled one: S00E09,
	// which TMDB hasn't, then S00E03.
	s.ask = AskTerminal(strings.NewReader("2\nS00E09\ns00e03\n"), &out)
	s.Run()

	show := filepath.Join(l.TV, "Ghosts (2019)", "Season 00")
	testutil.MustExist(t, show, "Ghosts (2019) - S00E04 - Series 2 Bloopers.mkv", "Ghosts (2019) - S00E03 - Series 1 Bloopers.mkv")
	for _, want := range []string{"TMDB has no S02E04. Which episode of Ghosts (2019) is it?", "1) S00E03  Series 1 Bloopers",
		"TMDB has no S00E09 either"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("asked without %q:\n%s", want, out.String())
		}
	}
	testutil.MustNotExist(t, l.Import, item)
}
