//go:build webui && files

package server

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/library"
	"mvd/internal/testutil"
)

// Needs you on the page: a card with the import's candidates and a search,
// the dialog, and Import (refused when the item has changed).
func TestUINeeds(t *testing.T) {
	ts, _ := newImportServer(t)
	ts.uploadFile("Dune.mkv")
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't import")
	}
	sig := ts.leftNow()[0].Sig
	h := body(t, ts.do("GET", "/incoming", "", nil))
	for _, want := range []string{`<h2 id="needs-h">Needs you</h2>`, `data-item="Dune.mkv" hx-preserve`, "Couldn&#39;t tell which this is.",
		`hx-get="/ui/import/details?item=Dune.mkv&amp;sig=` + sig + `&amp;kind=movie&amp;tmdb=438631"`, "Dune (2021)", "88%",
		`href="https://www.themoviedb.org/movie/438631" target="_blank"`, `hx-get="/ui/import/search"`,
		`<input type="hidden" name="kind" value="movie">`, `id="nav-needs" title="Needs you">1</span>`} {
		if !strings.Contains(h, want) {
			t.Errorf("card lacks %s", want)
		}
	}

	h = body(t, ts.do("GET", "/ui/import/search?item=Dune.mkv&sig="+sig+"&kind=movie&q=Get+Carter", "", nil))
	if !strings.Contains(h, "Get Carter (1971)") || !strings.Contains(h, "Get Carter (2000)") {
		t.Errorf("search:\n%s", h)
	}
	r := ts.do("GET", "/ui/import/search?item=Dune.mkv&sig="+sig+"&q="+url.QueryEscape("tmdb:841")+"&kind=movie", "", nil)
	if h := body(t, r); r.Header.Get("HX-Retarget") != "#import-dialog-body" || !strings.Contains(h, "Dune (1984)") {
		t.Errorf("a reference opens the dialog: %v\n%s", r.Header, h)
	}

	h = body(t, ts.do("GET", "/ui/import/details?item=Dune.mkv&sig="+sig+"&kind=movie&tmdb=438631", "", nil))
	if !inOrder(h, `src="/api/import/poster/movie/438631"`, "Dune (2021)", "2h", "Into Movies", "from Dune.mkv",
		`hx-post="/ui/import/choose"`, `name="sig" value="`+sig+`"`, ">Import</button>", ">Cancel</button>") {
		t.Errorf("dialog:\n%s", h)
	}

	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	r = ts.do("POST", "/ui/import/choose", "item=Dune.mkv&sig=0&kind=movie&tmdb=438631&title=Dune+(2021)", form)
	if h := body(t, r); r.Header.Get("HX-Retarget") != "#import-dialog-err" || !strings.Contains(h, "Nothing was imported: it&#39;s changed since") {
		t.Errorf("stale Import: %v\n%s", r.Header, h)
	}
	r = ts.do("POST", "/ui/import/choose", "item=Dune.mkv&sig="+sig+"&kind=movie&tmdb=438631&title=Dune+(2021)", form)
	if h := body(t, r); r.Header.Get("HX-Trigger") != "import-chosen" || !strings.Contains(h, "Importing Dune.mkv as Dune (2021).") {
		t.Errorf("Import: %v\n%s", r.Header, h)
	}
	importMu.Lock() // let the import it started finish
	importMu.Unlock()
}

// "in trash" in the head offers to empty it; emptying deletes what mvd put
// there, and nothing else.
func TestUIEmptyTrash(t *testing.T) {
	ts, l := newImportServer(t)
	testutil.Write(t, filepath.Join(l.Trash, "2026-11-01 120000 Old.Thing", "a.nfo"), "12345")
	testutil.Write(t, filepath.Join(l.Trash, "not mvd's.txt"), "x")
	ts.sv.imports.trashed(5<<30, false)
	h := body(t, ts.do("GET", "/incoming", "", nil))
	if !strings.Contains(h, `data-open="trash-dialog" data-size="5.0 GB">5.0 GB in trash</button>`) ||
		!strings.Contains(h, `hx-post="/ui/incoming/empty-trash"`) {
		t.Errorf("no trash link:\n%s", h)
	}
	r := ts.do("POST", "/ui/incoming/empty-trash", "", nil)
	if h := body(t, r); r.Header.Get("HX-Trigger") != "trash-emptied" || !strings.Contains(h, "Emptied the trash: 5.0 GB deleted.") ||
		strings.Contains(h, "in trash</button>") {
		t.Errorf("empty:\n%s", h)
	}
	testutil.MustNotExist(t, l.Trash, "2026-11-01 120000 Old.Thing")
	testutil.MustExist(t, l.Trash, "not mvd's.txt")
}

// A show's left videos on the page: each with why, Which episode? (the
// list, closest by title first) and Leave it out; File it files it.
func TestUINeedsEpisodes(t *testing.T) {
	ts, l := newImportServer(t)
	item := "Ghosts.2019.S02.720p.WEB-DL"
	ts.uploadFile(item + "/Ghosts.2019.S02E01.The.Grey.Lady.720p.WEB-DL.mkv")
	ts.uploadFile(item + "/Ghosts.2019.S02E03.The.Ghost.of.Xmas.720p.WEB-DL.mkv") // close, but not enough to file
	ts.sv.ups.mu.Lock()
	ts.sv.ups.finished[filepath.Join(l.Import, item)] = time.Now()
	ts.sv.ups.mu.Unlock()
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	left := ts.leftNow()
	if len(left) != 1 || len(left[0].Files) != 1 {
		t.Fatalf("left: %+v", left)
	}
	sig, video := left[0].Sig, left[0].Files[0].Path
	h := body(t, ts.do("GET", "/incoming", "", nil))
	if !inOrder(h, "say which episode each is", "Ghosts.2019.S02E03.The.Ghost.of.Xmas.720p.WEB-DL.mkv", "TMDB has no S02E03.",
		`hx-get="/ui/import/episodes"`, `name="tmdb" value="17174"`, ">Which episode?</button>",
		`hx-post="/ui/incoming/trash"`, `name="video" value="`+video+`"`, ">Leave it out</button>") {
		t.Errorf("card:\n%s", h)
	}

	q := "item=" + url.QueryEscape(item) + "&sig=" + sig + "&video=" + url.QueryEscape(video) + "&tmdb=17174"
	h = body(t, ts.do("GET", "/ui/import/episodes?"+q, "", nil))
	if !inOrder(h, `hx-post="/ui/import/episode"`, `<optgroup label="Closest by title"><option value="0:1">S00E01 · The Ghost of Christmas</option>`,
		`<optgroup label="Specials">`, `<optgroup label="Season 1">`, ">File it</button>") {
		t.Errorf("episodes:\n%s", h)
	}

	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	h = body(t, ts.do("POST", "/ui/import/episode", q+"&ep=0:1", form))
	if !strings.Contains(h, "Filing Ghosts.2019.S02E03.The.Ghost.of.Xmas.720p.WEB-DL.mkv as S00E01.") {
		t.Errorf("File it:\n%s", h)
	}
	want := "Ghosts (2019)/Season 00/Ghosts (2019) - S00E01 - The Ghost of Christmas.mkv"
	for i := 0; i < 100 && !library.Exists(filepath.Join(l.TV, want)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	importMu.Lock()
	importMu.Unlock()
	testutil.MustExist(t, l.TV, want)
	testutil.MustNotExist(t, l.Import, item)
}
