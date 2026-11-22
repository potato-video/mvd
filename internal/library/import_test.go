package library

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/config"
	"mvd/internal/testutil"
	"mvd/internal/tmdb"
)

func newTestImporter(t *testing.T, l testutil.Lib, srv *httptest.Server, o Options, src string) *Importer {
	cfg := config.Config{APIKey: "k", Language: "en-US", Region: "GB", CacheDir: t.TempDir(),
		MoviesDir: l.Movies, TVDir: l.TV, ImportDir: l.Import, TrashDir: l.Trash,
		TrashMaxAge: 7 * 24 * time.Hour, TrashMaxSize: 2 << 30, DataDir: l.Data}
	tm := tmdb.New(cfg)
	tm.SetBaseURLs(srv.URL+"/3", srv.URL+"/t/p/")
	if src == "" {
		src = l.Import
	}
	s := NewImporter(o, cfg, tm, src)
	s.settle = 0
	return s
}

func TestImport(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3 matroska"
	in := func(p, content string) { testutil.Write(t, filepath.Join(l.Import, p), content) }

	// A loose movie, scene-named.
	in("Children.of.Men.2006.1080p.BluRay.x264-GRP.mkv", mkv)
	// A release folder: its own .nfo, a sample, Subs/, featurettes, clutter.
	in("Dune.2021.2160p.UHD.BluRay-GRP/Dune.2021.2160p.UHD.BluRay-GRP.mkv", mkv)
	in("Dune.2021.2160p.UHD.BluRay-GRP/Dune.2021.2160p.UHD.BluRay-GRP.nfo", "release notes")
	in("Dune.2021.2160p.UHD.BluRay-GRP/RARBG.txt", "junk")
	in("Dune.2021.2160p.UHD.BluRay-GRP/Sample/dune-sample.mkv", mkv)
	in("Dune.2021.2160p.UHD.BluRay-GRP/Subs/2_English.srt", "1")
	in("Dune.2021.2160p.UHD.BluRay-GRP/Subs/3_English (SDH).srt", "1")
	in("Dune.2021.2160p.UHD.BluRay-GRP/Subs/4_French.srt", "1")
	in("Dune.2021.2160p.UHD.BluRay-GRP/Featurettes/Making of.mkv", mkv)
	// A season pack, with per-episode subtitle folders.
	in("Severance.S01.1080p.WEB-DL/Severance.S01E01.1080p.WEB-DL.mkv", mkv)
	in("Severance.S01.1080p.WEB-DL/Severance.S01E02.1080p.WEB-DL.mkv", mkv)
	in("Severance.S01.1080p.WEB-DL/Subs/Severance.S01E01.1080p.WEB-DL/2_English.srt", "1")
	in("Severance.S01.1080p.WEB-DL/Severance.S01.1080p.WEB-DL.nfo", "release notes")
	// A hand-made series: show from the folder, season from the subfolder.
	in("Doctor Who (2005)/Season 1/01 - Rose.mkv", mkv)
	in("Doctor Who (2005)/Season 1/02 - The End of the World.mkv", mkv)
	// A mixed dump: a movie and an episode.
	in("stuff/The.Godfather.Part.II.1974.mkv", mkv)
	in("stuff/Firefly.S01E02.mkv", mkv)
	// Forced TV: no markers, but it's in tv/.
	in("tv/Firefly/Episode 4.mkv", mkv)
	// Ambiguous (two Get Carters) but hinted.
	in("Get Carter/Get Carter.mkv", mkv)
	in("Get Carter/.mvd", "tmdb = 1102\n")
	// Ambiguous and not hinted: stays.
	in("Dune.mkv", mkv)
	// The same movie twice: the second is a duplicate.
	in("The Godfather 1972 REMUX/The.Godfather.1972.REMUX.mkv", mkv)
	in("The.Godfather.1972.1080p.mkv", mkv)
	// Not a video at all.
	in("readme.txt", "hello")
	// Mac litter, ignored.
	in("._Dune.mkv", "resource fork")

	// A dry run changes nothing.
	before := strings.Join(testutil.Tree(l.Video), "\n")
	s := newTestImporter(t, l, srv, Options{DryRun: true}, "")
	if rc := s.Run(); rc != 0 {
		t.Fatalf("dry run rc=%d", rc)
	}
	if after := strings.Join(testutil.Tree(l.Video), "\n"); after != before {
		t.Fatalf("dry run changed things:\n%s", after)
	}

	s = newTestImporter(t, l, srv, Options{}, "")
	logf, _ := os.Create(filepath.Join(l.Data, "import.log"))
	s.SetLog(logf)
	if rc := s.Run(); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	logf.Close()
	if s.Stats().Movies != 5 || s.Stats().Episodes != 6 || s.Stats().Duplicates != 1 {
		t.Errorf("movies=%d eps=%d dups=%d, want 5, 6, 1", s.Stats().Movies, s.Stats().Episodes, s.Stats().Duplicates)
	}

	testutil.MustExist(t, l.Movies,
		"Children of Men (2006)/Children of Men (2006).mkv",
		"Children of Men (2006)/Children of Men (2006).nfo",
		"Dune (2021)/Dune (2021).mkv",
		"Dune (2021)/Dune (2021).nfo",
		"Dune (2021)/Dune (2021).en.srt",
		"Dune (2021)/Dune (2021).en.sdh.srt",
		"Dune (2021)/Dune (2021).fr.srt",
		"Dune (2021)/featurettes/Making of.mkv",
		"Dune (2021)/poster.jpg",
		"The Godfather Part II (1974)/The Godfather Part II (1974).mkv",
		"Get Carter (1971)/Get Carter (1971).mkv",
		"The Godfather (1972)/The Godfather (1972).mkv",
	)
	testutil.MustExist(t, l.TV,
		"Severance (2022)/tvshow.nfo",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.mkv",
		"Severance (2022)/Season 01/Severance (2022) - S01E01 - Good News About Hell.en.srt",
		"Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop.mkv",
		"Doctor Who (2005)/Season 01/Doctor Who (2005) - S01E01 - Rose.mkv",
		"Doctor Who (2005)/Season 01/Doctor Who (2005) - S01E02 - The End of the World.mkv",
		"Firefly (2002)/Season 01/Firefly (2002) - S01E02 - Bushwhacked.mkv",
		"Firefly (2002)/Season 01/Firefly (2002) - S01E04 - Jaynestown.mkv",
	)
	// The release .nfo didn't come along as the movie's NFO.
	if b, _ := os.ReadFile(filepath.Join(l.Movies, "Dune (2021)/Dune (2021).nfo")); !strings.Contains(string(b), "<movie>") {
		t.Errorf("Dune's NFO is the release's:\n%s", b)
	}

	// What's left in import: the unsure, the duplicate, the non-video, and
	// the tv/ container; everything filed has gone, leftovers to the trash.
	testutil.MustExist(t, l.Import, "Dune.mkv", "readme.txt", "tv")
	testutil.MustNotExist(t, l.Import, "Children.of.Men.2006.1080p.BluRay.x264-GRP.mkv", "Dune.2021.2160p.UHD.BluRay-GRP",
		"Severance.S01.1080p.WEB-DL", "Doctor Who (2005)", "stuff", "tv/Firefly", "Get Carter")
	dups := 0
	for _, d := range []string{"The Godfather 1972 REMUX", "The.Godfather.1972.1080p.mkv"} {
		if Exists(filepath.Join(l.Import, d)) {
			dups++
		}
	}
	if dups != 1 {
		t.Errorf("one copy of The Godfather should be left, found %d", dups)
	}
	var trashed []string
	ents, _ := os.ReadDir(l.Trash)
	for _, e := range ents {
		trashed = append(trashed, e.Name()[len(trashStamp)+1:])
	}
	got := strings.Join(trashed, "|")
	for _, want := range []string{"Dune.2021.2160p.UHD.BluRay-GRP", "Severance.S01.1080p.WEB-DL", "Get Carter"} {
		if !strings.Contains(got, want) {
			t.Errorf("trash lacks %s: %s", want, got)
		}
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "Dune.2021.2160p.UHD.BluRay-GRP") {
			testutil.MustExist(t, filepath.Join(l.Trash, e.Name()), "RARBG.txt", "Sample/dune-sample.mkv",
				"Dune.2021.2160p.UHD.BluRay-GRP.nfo")
		}
	}

	// The log says what went where.
	log, _ := os.ReadFile(filepath.Join(l.Data, "import.log"))
	if !strings.Contains(string(log), "\tmove\t"+filepath.Join(l.Import, "Children.of.Men.2006.1080p.BluRay.x264-GRP.mkv")) {
		t.Errorf("import log:\n%s", log)
	}

	// Later: a new episode joins its show; nothing else changes.
	in("Severance.S01E04.mkv", mkv)
	s = newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	if s.Stats().Movies != 0 || s.Stats().Episodes != 1 {
		t.Errorf("second run: movies=%d eps=%d", s.Stats().Movies, s.Stats().Episodes)
	}
	testutil.MustExist(t, l.TV, "Severance (2022)/Season 01/Severance (2022) - S01E04 - The You You Are.mkv")
	if n := len(testutil.Tree(filepath.Join(l.TV))); n == 0 {
		t.Fatal("tv vanished")
	}

	t.Logf("library:\n%s", strings.Join(testutil.Tree(l.Video), "\n"))
}

func TestImportCopyAndArriving(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	stick := t.TempDir()
	testutil.Write(t, filepath.Join(stick, "Boogie.Nights.1997.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(stick, "Boogie.Nights.1997.en.srt"), "1")

	s := newTestImporter(t, l, srv, Options{Copy: true}, stick)
	s.Run()
	testutil.MustExist(t, l.Movies, "Boogie Nights (1997)/Boogie Nights (1997).mkv", "Boogie Nights (1997)/Boogie Nights (1997).en.srt")
	testutil.MustExist(t, stick, "Boogie.Nights.1997.mkv", "Boogie.Nights.1997.en.srt")

	// Again: it's a duplicate now, and the source is still left alone.
	s = newTestImporter(t, l, srv, Options{Copy: true}, stick)
	s.Run()
	if s.Stats().Duplicates != 1 || s.Stats().Movies != 0 {
		t.Errorf("second copy: dups=%d movies=%d", s.Stats().Duplicates, s.Stats().Movies)
	}

	// A file that has only just landed waits.
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Mission.Impossible.1996.mkv.part"), "half")
	s = newTestImporter(t, l, srv, Options{}, "")
	s.settle = time.Hour
	s.Run()
	if s.Stats().Waiting != 2 || s.Stats().Movies != 0 {
		t.Errorf("arriving: wait=%d movies=%d", s.Stats().Waiting, s.Stats().Movies)
	}
	testutil.MustExist(t, l.Import, "Fast.Five.2011.mkv")

	// Unless it's known to be complete (an upload the server has finished).
	s = newTestImporter(t, l, srv, Options{}, "")
	s.settle = time.Hour
	s.SetComplete([]string{filepath.Join(l.Import, "Fast.Five.2011.mkv")})
	s.Run()
	if s.Stats().Movies != 1 || s.Stats().Waiting != 1 {
		t.Errorf("known complete: movies=%d wait=%d", s.Stats().Movies, s.Stats().Waiting)
	}
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")
}

func TestImportNoTrash(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.1080p/Fast.Five.2011.1080p.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Fast.Five.2011.1080p/junk.txt"), "x")
	s := newTestImporter(t, l, srv, Options{}, "")
	s.cfg.TrashMaxAge = 0
	s.Run()
	testutil.MustExist(t, l.Movies, "Fast Five (2011)/Fast Five (2011).mkv")
	testutil.MustNotExist(t, l.Import, "Fast.Five.2011.1080p")
	if Exists(l.Trash) {
		t.Errorf("trash used with trash_max_age = 0")
	}
}

func TestPurgeTrash(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(age time.Duration, name string, size int) {
		testutil.Write(t, filepath.Join(dir, now.Add(-age).Format(trashStamp)+" "+name, "f"), strings.Repeat("x", size))
	}
	mk(10*24*time.Hour, "ancient", 10)
	mk(3*24*time.Hour, "old", 600)
	mk(2*24*time.Hour, "middle", 300)
	mk(time.Hour, "new", 300)
	if n := purgeTrash(dir, 7*24*time.Hour, 700, now); n != 2 {
		t.Errorf("removed %d, want 2 (ancient by age, old by size)", n)
	}
	var left []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		left = append(left, e.Name()[len(trashStamp)+1:])
	}
	if strings.Join(left, ",") != "middle,new" {
		t.Errorf("left %v", left)
	}
	if n := purgeTrash(dir, 0, 700, now); n != 2 {
		t.Errorf("max age 0 should empty it, removed %d", n)
	}
}

func TestPlanItem(t *testing.T) {
	root := t.TempDir()
	mkv := "\x1a\x45\xdf\xa3"
	testutil.Write(t, filepath.Join(root, "Pack/Show.Name.S02.720p/Show.Name.S02E01.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Pack/Show.Name.S02.720p/Show.Name.S02E02E03.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Pack/Other Show/Season 3/05.mkv"), mkv)
	testutil.Write(t, filepath.Join(root, "Pack/Seven Samurai (1954)/Seven.Samurai.1954.CD1.avi"), "RIFF....AVI ")
	testutil.Write(t, filepath.Join(root, "Pack/Seven Samurai (1954)/Seven.Samurai.1954.CD2.avi"), "RIFF....AVI ")
	testutil.Write(t, filepath.Join(root, "Pack/movie-trailer.mkv"), mkv)

	p := planItem(filepath.Join(root, "Pack"), true, "")
	if len(p.shows) != 2 || len(p.movies) != 1 {
		t.Fatalf("shows=%d movies=%d", len(p.shows), len(p.movies))
	}
	a, b := p.shows[0], p.shows[1]
	if a.parsed.Title != "Show Name" {
		a, b = b, a
	}
	if a.parsed.Title != "Show Name" || len(a.eps) != 2 || a.eps[1].season != 2 || len(a.eps[1].eps) != 2 {
		t.Errorf("show a: %+v %+v", a.parsed, a.eps)
	}
	if b.parsed.Title != "Other Show" || b.eps[0].season != 3 || b.eps[0].eps[0] != 5 {
		t.Errorf("show b: %+v %+v", b.parsed, b.eps[0])
	}
	f := p.movies[0]
	if f.parsed.Title != "Seven Samurai" || f.parsed.Year != 1954 || len(f.videos) != 2 {
		t.Errorf("movie: %+v %v", f.parsed, f.videos)
	}
	if len(p.unknown) != 1 || !strings.HasSuffix(p.unknown[0], "movie-trailer.mkv") {
		t.Errorf("unknown: %v", p.unknown)
	}
}

func TestSubSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"2_English.srt":             ".en.srt",
		"English (SDH).srt":         ".en.sdh.srt",
		"eng.forced.srt":            ".en.forced.srt",
		"3_Brazilian.srt":           ".pt-BR.srt",
		"Forced.srt":                ".forced.srt",
		"French.ass":                ".fr.ass",
		"something odd.srt":         ".srt",
		"14_Chinese.Simplified.srt": ".zh.srt",
	} {
		if got := subSuffix(in); got != want {
			t.Errorf("subSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportHintsAndPrompt(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3"
	// A hint with a DVD order: the new show is filed in that order, and
	// remembers it.
	testutil.Write(t, filepath.Join(l.Import, "BTAS/btas.s01e01.mkv"), mkv+"leather")
	testutil.Write(t, filepath.Join(l.Import, "BTAS/btas.s01e02.mkv"), mkv+"joker")
	testutil.Write(t, filepath.Join(l.Import, "BTAS/.mvd"), "kind = \"tv\"\ntmdb = 2098\norder = \"dvd\"\n")
	// Unsure, so it asks: pick the second candidate.
	testutil.Write(t, filepath.Join(l.Import, "Dune.mkv"), mkv)

	s := newTestImporter(t, l, srv, Options{}, "")
	s.ask = AskTerminal(strings.NewReader("1\n2\n"), s.writer()) // DVD order 1 of 2; then Dune candidate 2
	s.Run()

	show := filepath.Join(l.TV, "Batman - The Animated Series (1992)")
	if b, _ := os.ReadFile(filepath.Join(show, "Season 01/Batman - The Animated Series (1992) - S01E01 - On Leather Wings.mkv")); !strings.HasSuffix(string(b), "leather") {
		t.Errorf("DVD order not used:\n%s", strings.Join(testutil.Tree(l.TV), "\n"))
	}
	if kv := readShowSettings(show); kv["order"] != "dvd" || kv["group"] != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("show .mvd: %v", kv)
	}
	if s.Stats().Movies != 1 || len(testutil.Tree(l.Movies)) == 0 {
		t.Errorf("prompted movie not filed: %d\n%s", s.Stats().Movies, strings.Join(testutil.Tree(l.Movies), "\n"))
	}
	testutil.MustNotExist(t, l.Import, "BTAS", "Dune.mkv")
}

func TestImportResults(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	mkv := "\x1a\x45\xdf\xa3"
	in := func(p string) { testutil.Write(t, filepath.Join(l.Import, p), mkv) }
	in("Children.of.Men.2006.mkv")                                         // filed
	in("Dune.mkv")                                                         // not sure: candidates
	in("stuff/Fast.Five.2011.mkv")                                         // half filed:
	in("stuff/Firefly.S01E02.mkv")                                         // a movie and an episode filed,
	in("stuff/Dune.mkv")                                                   // one not sure,
	in("stuff/movie-trailer.mkv")                                          // and one it can't place
	in("Severance.S01/Severance.S01E01.mkv")                               // filed, and
	in("Severance.S01/Severance.S01E01.1080p.mkv")                         // the same episode again
	testutil.Write(t, filepath.Join(l.Import, "readme.txt"), "hello")      // no videos
	testutil.Write(t, filepath.Join(l.Import, "Heat.1995.part2.rar"), "x") // no first volume
	testutil.Write(t, filepath.Join(l.Import, "Arriving.2020.mkv.part"), "half")

	s := newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	// The same movie again, now in the library.
	in("Children.of.Men.2006.1080p.mkv")
	s2 := newTestImporter(t, l, srv, Options{}, "")
	s2.Run()

	got := map[string]ItemResult{}
	for _, r := range s.Results() {
		got[filepath.Base(r.Path)] = r
	}
	var again ItemResult
	for _, r := range s2.Results() {
		if filepath.Base(r.Path) == "Children.of.Men.2006.1080p.mkv" {
			again = r
		}
	}
	unit := func(r ItemResult, i int) UnitResult {
		t.Helper()
		if i >= len(r.Units) {
			t.Fatalf("%s: %d units, want more: %+v", r.Label, len(r.Units), r)
		}
		return r.Units[i]
	}

	if r := got["Children.of.Men.2006.mkv"]; r.State != ItemFiled || unit(r, 0).State != UnitFiled ||
		unit(r, 0).Title != "Children of Men (2006)" || unit(r, 0).Dest != filepath.Join(l.Movies, "Children of Men (2006)") {
		t.Errorf("filed: %+v", r)
	}
	if r := got["Dune.mkv"]; r.State != ItemLeft || unit(r, 0).State != UnitChoose || len(unit(r, 0).Candidates) < 2 {
		t.Errorf("not sure: %+v", r)
	}
	if r := got["stuff"]; r.State != ItemLeft || len(r.Units) != 3 || unit(r, 0).State != UnitChoose ||
		unit(r, 1).State != UnitFiled || unit(r, 2).Episodes != 1 || len(r.Unknown) != 1 ||
		len(r.Unfiled) != 2 || r.Trashed {
		t.Errorf("half filed: %+v", r)
	}
	if r := got["Severance.S01"]; r.State != ItemLeft || unit(r, 0).Kind != "tv" || unit(r, 0).Episodes != 1 ||
		unit(r, 0).Duplicates != 1 || len(r.Unfiled) != 1 {
		t.Errorf("show: %+v", r)
	}
	if r := got["readme.txt"]; r.State != ItemLeft || r.Problem != NoVideos {
		t.Errorf("no videos: %+v", r)
	}
	want := MissingVolume
	if !unpackBuilt {
		want = NotUnpackable
	}
	if r := got["Heat.1995.part2.rar"]; r.State != ItemLeft || r.Problem != want {
		t.Errorf("missing volume: %+v", r)
	}
	if r := got["Arriving.2020.mkv.part"]; r.State != ItemWaiting || !strings.Contains(r.Detail, "still downloading") {
		t.Errorf("arriving: %+v", r)
	}
	if r := again; r.State != ItemLeft || unit(r, 0).State != UnitDuplicate ||
		unit(r, 0).Dest != filepath.Join(l.Movies, "Children of Men (2006)") {
		t.Errorf("duplicate: %+v", r)
	}
	if st := s.Stats(); st.Movies != 2 || st.Episodes != 2 || st.Left != 5 || st.Duplicates != 1 || st.Waiting != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestIdentifyThenFile(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	testutil.Write(t, filepath.Join(l.Import, "Children.of.Men.2006.mkv"), "\x1a\x45\xdf\xa3")
	testutil.Write(t, filepath.Join(l.Import, "Dune.mkv"), "\x1a\x45\xdf\xa3")
	s := newTestImporter(t, l, srv, Options{}, "")
	s.scanLibrary()
	s.loadDownloads()

	sure := s.Identify(filepath.Join(l.Import, "Children.of.Men.2006.mkv"), "")
	unsure := s.Identify(filepath.Join(l.Import, "Dune.mkv"), "")
	if len(testutil.Tree(l.Movies)) != 0 {
		t.Fatalf("Identify filed something:\n%s", strings.Join(testutil.Tree(l.Movies), "\n"))
	}
	if sure.Result.State != "" || unsure.Result.State != "" {
		t.Errorf("identified as finished already: %+v, %+v", sure.Result, unsure.Result)
	}

	if r := s.File(sure); r.State != ItemFiled {
		t.Errorf("sure: %+v", r)
	}
	testutil.MustExist(t, l.Movies, "Children of Men (2006)/Children of Men (2006).mkv")
	if r := s.File(unsure); r.State != ItemLeft || len(r.Units) != 1 || r.Units[0].State != UnitChoose {
		t.Errorf("unsure: %+v", r)
	}
	testutil.MustExist(t, l.Import, "Dune.mkv")
}

func TestCheckImport(t *testing.T) {
	l := testutil.NewLib(t)
	cfg := config.Config{APIKey: "k", MoviesDir: l.Movies, TVDir: l.TV, TrashDir: l.Trash,
		TrashMaxAge: time.Hour, TrashMaxSize: 1 << 30}
	if err := CheckImport(cfg, l.Import); err != nil {
		t.Errorf("import/: %v", err)
	}
	for name, src := range map[string]string{
		"in the library":      filepath.Join(l.Movies, "Dune (2021)"),
		"holding the library": l.Video,
	} {
		if err := CheckImport(cfg, src); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	inside := cfg
	inside.TrashDir = filepath.Join(l.Import, ".trash")
	if err := CheckImport(inside, l.Import); err == nil {
		t.Errorf("the trash in import/: no error")
	}
	noKey := cfg
	noKey.APIKey = ""
	if err := CheckImport(noKey, l.Import); err == nil || !strings.Contains(err.Error(), "TMDB API key") {
		t.Errorf("no key: %v", err)
	}
}

// Needs you, end to end: an import leaves an unsure item with its
// candidates; choosing one writes a hint; the next import files it.
func TestHintSettlesAChoice(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	item := filepath.Join(l.Import, "Dune.mkv")
	testutil.Write(t, item, "\x1a\x45\xdf\xa3")

	s := newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	r := s.Results()[0]
	if r.State != ItemLeft || r.Units[0].State != UnitChoose {
		t.Fatalf("first import: %+v", r)
	}
	var pick Candidate
	for _, c := range r.Units[0].Candidates {
		if c.Year() == 1984 {
			pick = c
		}
	}
	if err := WriteHint(r.Path, r.Units[0].Kind, pick.ID); err != nil {
		t.Fatal(err)
	}

	s = newTestImporter(t, l, srv, Options{}, "")
	s.Run()
	if r := s.Results()[0]; r.State != ItemFiled {
		t.Errorf("second import: %+v", r)
	}
	testutil.MustExist(t, l.Movies, "Dune (1984)/Dune (1984).mkv")
	testutil.MustNotExist(t, l.Import, "Dune.mkv", "Dune.mvd")
}

// The website's 🗑: an import item goes to the trash, with its hint.
func TestTrashItem(t *testing.T) {
	l := testutil.NewLib(t)
	cfg := config.Config{MoviesDir: l.Movies, TVDir: l.TV, ImportDir: l.Import, TrashDir: l.Trash,
		TrashMaxAge: time.Hour, TrashMaxSize: 1 << 30}
	testutil.Write(t, filepath.Join(l.Import, "Dune.mkv"), "12345")
	testutil.Write(t, filepath.Join(l.Import, "Dune.mvd"), "kind = \"movie\"\n")
	n, deleted, err := TrashItem(cfg, filepath.Join(l.Import, "Dune.mkv"))
	if err != nil || deleted || n != 5+int64(len("kind = \"movie\"\n")) {
		t.Fatalf("%d, %v", n, err)
	}
	testutil.MustNotExist(t, l.Import, "Dune.mkv", "Dune.mvd")
	if got := len(testutil.Tree(l.Trash)); got != 2 {
		t.Errorf("trash: %v", testutil.Tree(l.Trash))
	}
}

// Review L7: two things of the same name trashed in the same second are
// both kept (a rename onto the first replaced it); and with the trash off,
// TrashItem says it deleted rather than moved.
func TestTrashKeepsBothAndSaysWhenItDeletes(t *testing.T) {
	l := testutil.NewLib(t)
	for i := 0; i < 3; i++ {
		testutil.Write(t, filepath.Join(l.Import, "x.mvd"), fmt.Sprint(i))
		if _, deleted, err := moveToTrash(l.Trash, filepath.Join(l.Import, "x.mvd")); err != nil || deleted {
			t.Fatal(deleted, err)
		}
	}
	if got := testutil.Tree(l.Trash); len(got) != 3 {
		t.Errorf("trash: %v", got)
	}

	cfg := config.Config{MoviesDir: l.Movies, TVDir: l.TV, ImportDir: l.Import}
	testutil.Write(t, filepath.Join(l.Import, "Dune.mkv"), "12345")
	if _, deleted, err := TrashItem(cfg, filepath.Join(l.Import, "Dune.mkv")); err != nil || !deleted {
		t.Errorf("trash off: deleted %v, %v", deleted, err)
	}
	testutil.MustNotExist(t, l.Import, "Dune.mkv")
}

// Review L8: a copy over SMB makes its folder before its first file, so a
// new, empty folder waits like a new file; an old one is cleared away.
func TestImportNewEmptyFolderWaits(t *testing.T) {
	srv := testutil.TMDBAll(t)
	l := testutil.NewLib(t)
	os.MkdirAll(filepath.Join(l.Import, "Just Made"), 0o755)
	testutil.Write(t, filepath.Join(l.Import, "Mac Copy", ".DS_Store"), "x")
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(l.Import, "Mac Copy", ".DS_Store"), old, old)
	os.MkdirAll(filepath.Join(l.Import, "Long Empty"), 0o755)
	os.Chtimes(filepath.Join(l.Import, "Long Empty"), old, old)
	s := newTestImporter(t, l, srv, Options{}, "")
	s.settle = time.Hour
	s.Run()
	testutil.MustExist(t, l.Import, "Just Made", "Mac Copy")
	testutil.MustNotExist(t, l.Import, "Long Empty")
}
