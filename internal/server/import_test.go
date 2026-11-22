package server

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mvd/internal/library"
	"mvd/internal/testutil"
)

// uploadFile puts a small video in the import folder as a finished upload
// would be, so an import takes it at once (no settling).
func (ts *testServer) uploadFile(name string) {
	ts.t.Helper()
	p := filepath.Join(ts.sv.cfg.ImportDir, name)
	testutil.Write(ts.t, p, "\x1a\x45\xdf\xa3"+strings.Repeat("x", 996))
	ts.sv.ups.mu.Lock()
	if ts.sv.ups.finished == nil {
		ts.sv.ups.finished = map[string]time.Time{}
	}
	ts.sv.ups.finished[p] = time.Now()
	ts.sv.ups.mu.Unlock()
}

// resultFor is the last import's result for an item, if it has one.
func (ts *testServer) resultFor(name string) (library.ItemResult, bool) {
	rs, _ := ts.sv.lastImport()
	for _, r := range rs {
		if filepath.Base(r.Path) == name {
			return r, true
		}
	}
	return library.ItemResult{}, false
}

// An item the import isn't sure of keeps its candidates until the next
// import, which (once it's been answered) files it, and it goes.
func TestImportResultsKept(t *testing.T) {
	ts, l := newImportServer(t)
	ts.uploadFile("Dune.mkv")
	if !ts.sv.autoImportOnce() {
		t.Fatal("didn't run")
	}
	check := func(when string) {
		t.Helper()
		r, ok := ts.resultFor("Dune.mkv")
		if !ok || r.State != library.ItemLeft || len(r.Units) != 1 || r.Units[0].State != library.UnitChoose ||
			len(r.Units[0].Candidates) < 2 {
			t.Fatalf("%s: %+v", when, r)
		}
	}
	check("after the import")
	if ts.sv.autoImportOnce() {
		t.Fatal("ran again with nothing new")
	}
	check("a poll later")

	if err := library.WriteHint(filepath.Join(l.Import, "Dune.mkv"), "movie", 438631); err != nil {
		t.Fatal(err)
	}
	if !ts.sv.autoImportOnce() {
		t.Fatal("a hint written didn't start an import")
	}
	testutil.MustExist(t, l.Movies, "Dune (2021)/Dune (2021).mkv")
	if r, ok := ts.resultFor("Dune.mkv"); ok && r.State != library.ItemFiled {
		t.Errorf("still there: %+v", r)
	}
	if _, stopped := ts.sv.lastImport(); stopped != "" {
		t.Errorf("stopped: %s", stopped)
	}
}

// A folder's .mvd hint is a change auto-import notices.
func TestImportScanHint(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, "Dune", "Dune.mkv"), "x")
	testutil.Write(t, filepath.Join(dir, "movies", "Heat", "Heat.mkv"), "xy")
	testutil.Write(t, filepath.Join(dir, "movies", "Direct.mkv"), "xy")
	testutil.Write(t, filepath.Join(dir, "Loose.mkv"), "xyz")
	testutil.Write(t, filepath.Join(dir, "Loose.mvd"), "kind = \"movie\"\n")
	testutil.Write(t, filepath.Join(dir, ".uploads", "abc.part"), "partial")
	before := scanImport(dir)
	var got []string
	for _, it := range before.Items {
		got = append(got, it.Kind+":"+it.Name)
	}
	if want := []string{":Dune", ":Loose.mkv", "movie:Direct.mkv", "movie:Heat"}; !slices.Equal(got, want) {
		t.Errorf("items: %v, want %v", got, want)
	}
	if it := before.item(filepath.Join(dir, "movies", "Heat")); it == nil || !it.Dir || it.Size != 2 || it.Files != 1 {
		t.Errorf("Heat: %+v", it)
	}
	testutil.Write(t, filepath.Join(dir, "Dune", ".mvd"), "kind = \"movie\"\ntmdb = 438631\n")
	after := scanImport(dir)
	if after.Sig == before.Sig || after.item(filepath.Join(dir, "Dune")).Sig == before.item(filepath.Join(dir, "Dune")).Sig {
		t.Error("a hint didn't change the signature")
	}
	if after.item(filepath.Join(dir, "Loose.mkv")).Sig != before.item(filepath.Join(dir, "Loose.mkv")).Sig {
		t.Error("another item's signature changed")
	}

	heat := filepath.Join(dir, "movies", "Heat")
	testutil.Write(t, filepath.Join(heat, ".mvd"), "kind = \"movie\"\ntmdb = 438631\n")
	withFolderHint := scanImport(dir)
	if withFolderHint.item(heat).Sig == after.item(heat).Sig {
		t.Error("a movies/ folder hint didn't change the item's signature")
	}

	direct := filepath.Join(dir, "movies", "Direct.mkv")
	testutil.Write(t, filepath.Join(dir, "movies", "Direct.mvd"), "kind = \"movie\"\ntmdb = 438631\n")
	withDirectHint := scanImport(dir)
	if withDirectHint.item(direct).Sig == withFolderHint.item(direct).Sig {
		t.Error("a movies/ file hint didn't change the item's signature")
	}

	testutil.Write(t, filepath.Join(dir, "Loose.mvd"), "kind = \"movie\"\ntmdb = 438631\n")
	withLooseHint := scanImport(dir)
	if withLooseHint.item(filepath.Join(dir, "Loose.mkv")).Sig == withDirectHint.item(filepath.Join(dir, "Loose.mkv")).Sig {
		t.Error("a loose video's hint didn't change the item's signature")
	}
}

// Failures are counted per item, in a row, while it stays the same; an
// import that stopped short (TMDB out of reach) counts none, and keeps
// what the earlier one said about the items it didn't get to.
func TestImportFailureCounts(t *testing.T) {
	var im imports
	scan := func(sig uint64) importScan {
		return importScan{Items: []scannedItem{{Path: "/i/A", Sig: sig}, {Path: "/i/B", Sig: 1}}}
	}
	failed := library.ItemResult{Path: "/i/A", State: library.ItemLeft,
		Units: []library.UnitResult{{State: library.UnitFailed, Err: "disk full"}}}
	fine := library.ItemResult{Path: "/i/B", State: library.ItemWaiting}
	for i := 0; i < 3; i++ {
		im.keep([]library.ItemResult{failed, fine}, nil, scan(7))
	}
	if n := im.fails["/i/A"].n; n != 3 {
		t.Errorf("3 failures counted as %d", n)
	}
	if _, retry := im.settle(); retry {
		t.Error("retrying an item that has failed 3 times")
	}
	im.keep([]library.ItemResult{failed, fine}, nil, scan(8))
	if n := im.fails["/i/A"].n; n != 1 {
		t.Errorf("changed, then failed: counted %d", n)
	}
	if _, retry := im.settle(); !retry {
		t.Error("not retrying a first failure")
	}

	im.keep([]library.ItemResult{failed}, errors.New("can't reach TMDB"), scan(8))
	if im.stopped == "" || len(im.results) != 2 || im.fails["/i/A"].n != 1 {
		t.Errorf("stopped short: %q, %d results, %+v", im.stopped, len(im.results), im.fails)
	}

	im.keep([]library.ItemResult{fine}, nil, scan(8))
	if _, ok := im.fails["/i/A"]; ok || im.stopped != "" {
		t.Errorf("gone, but still counted: %q %+v", im.stopped, im.fails)
	}
}

// The trash's size comes from an import (which empties it of what's old),
// plus whatever's been put there since.
func TestImportTrashSize(t *testing.T) {
	ts, _ := newImportServer(t)
	if _, ok := ts.sv.trashSize(); ok {
		t.Error("known before any import")
	}
	ts.sv.imports.trashed(5, true)
	if _, ok := ts.sv.trashSize(); ok {
		t.Error("known from growth alone")
	}
	ts.uploadFile("Fast.Five.2011.mkv")
	if _, err := ts.sv.runImport(); err != nil {
		t.Fatal(err)
	}
	if n, ok := ts.sv.trashSize(); !ok || n != 0 {
		t.Errorf("after an import: %d %v", n, ok)
	}
	ts.sv.imports.trashed(5, true)
	if n, ok := ts.sv.trashSize(); !ok || n != 5 {
		t.Errorf("grown: %d %v", n, ok)
	}
}

// Review L2: an import folder given with a trailing slash gives the same
// items, not names missing their first letter.
func TestImportScanTrailingSlash(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, "abc.mkv"), "x")
	testutil.Write(t, filepath.Join(dir, "Folder", "f.mkv"), "x")
	testutil.Write(t, filepath.Join(dir, "movies", "Heat.mkv"), "x")
	var got []string
	for _, it := range scanImport(dir + "/").Items {
		got = append(got, it.Kind+":"+it.Name)
	}
	if want := []string{":Folder", ":abc.mkv", "movie:Heat.mkv"}; !slices.Equal(got, want) {
		t.Errorf("items: %v, want %v", got, want)
	}
}
