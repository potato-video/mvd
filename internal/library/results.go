package library

// What an import did, item by item: the CLI prints it, the server keeps it
// (the website's Incoming page and its Needs you).

// ItemState is where an import item got to.
type ItemState string

const (
	ItemFiled   ItemState = "filed"   // everything in it filed (or nothing left in it to file)
	ItemWaiting ItemState = "waiting" // still arriving or downloading: Detail says which
	ItemSeeding ItemState = "seeding" // a finished download still seeding: Detail says how far
	ItemLeft    ItemState = "left"    // left for a person: see Problem, Units, Unknown and Unfiled
	ItemRemoved ItemState = "removed" // an empty folder, removed
	ItemPending ItemState = "pending" // dry run: its archives would be unpacked first
)

// Problem is why an item was left, when it's about the whole item rather
// than a movie or show in it.
type Problem string

const (
	NoVideos      Problem = "no-videos"      // nothing in it is a video
	CantUnpack    Problem = "cant-unpack"    // an archive that failed: Detail says why
	NotUnpackable Problem = "not-unpackable" // only archives mvd doesn't unpack (.7z, .001, or a build without unpacking)
	MissingVolume Problem = "missing-volume" // part of a split archive whose first volume isn't here
	CopyArchives  Problem = "copy-archives"  // archives, which -copy doesn't unpack
)

// UnitState is where one movie or show in an item got to.
type UnitState string

const (
	UnitFiled     UnitState = "filed"     // filed (a show: Episodes of them)
	UnitChoose    UnitState = "choose"    // not a confident match: Candidates (none: nothing found)
	UnitSkipped   UnitState = "skipped"   // asked, and skipped
	UnitDuplicate UnitState = "duplicate" // already in the library, at Dest
	UnitNotFiled  UnitState = "not-filed" // matched, but none of its files could be filed
	UnitFailed    UnitState = "failed"    // Err says why
)

// UnitResult is one movie or show in an item.
type UnitResult struct {
	Label      string
	Kind       string // "movie" or "tv"
	State      UnitState
	TMDB       int
	Title      string // "Dune (2021)"
	Dest       string // its folder in the library (filed, or the copy already there)
	Episodes   int    // TV: episodes filed
	Duplicates int    // TV: episodes already in the library
	Candidates []Candidate
	Err        string

	changed bool // the library changed: worth a rescan
}

// ItemResult is what became of one entry in the import folder.
type ItemResult struct {
	Path    string // the item (a loose archive: the folder it was unpacked in)
	Label   string
	State   ItemState
	Problem Problem
	Detail  string
	Units   []UnitResult
	Unknown []string          // videos it couldn't tell are movies or episodes
	Unfiled []string          // videos not filed, in an item left with some filed
	Why     map[string]string // why a video in a matched show wasn't filed, by its path: "TMDB has no S02E07"
	Trashed bool              // its leftovers went to the trash
}

// importStats counts up an import's results.
func importStats(rs []ItemResult) Stats {
	var st Stats
	for _, r := range rs {
		switch r.State {
		case ItemLeft:
			st.Left++
		case ItemWaiting:
			st.Waiting++
		case ItemSeeding:
			st.Seeding++
		}
		for _, u := range r.Units {
			st.Episodes += u.Episodes
			st.Duplicates += u.Duplicates
			if u.changed {
				st.Changed++
			}
			switch u.State {
			case UnitFiled:
				if u.Kind == "movie" {
					st.Movies++
				}
			case UnitDuplicate:
				if u.Kind == "movie" {
					st.Duplicates++
				}
			case UnitFailed:
				st.Failed++
			}
		}
	}
	return st
}

// Reason says why an item with a Problem was left.
func (r ItemResult) Reason() string {
	switch r.Problem {
	case NoVideos:
		return "no videos in it"
	case CantUnpack:
		return r.Detail
	case NotUnpackable:
		if !unpackBuilt {
			return "archives only, and this mvd was built without unpacking"
		}
		return "archives mvd can't unpack (.7z, or split .001 files)"
	case MissingVolume:
		return "part of a split archive whose first volume isn't here"
	case CopyArchives:
		return "archives, which -copy doesn't unpack (it never writes to the source)"
	}
	return ""
}

// printItem says what became of an item, after the commentary on what was
// done to it: … waiting, ? left, - not sure, = a duplicate, ! failed.
func (s *Importer) printItem(r ItemResult) {
	switch {
	case r.State == ItemWaiting, r.State == ItemSeeding:
		s.printf("… %s: %s\n", r.Label, r.Detail)
	case r.Problem != "":
		s.printf("? %s: %s; left as is\n", r.Label, r.Reason())
	}
	for _, u := range r.Units {
		switch u.State {
		case UnitChoose:
			s.printf("- %s: no confident match, skipped\n", u.Label)
		case UnitSkipped:
			s.printf("  %s: left in import\n", u.Label)
		case UnitDuplicate:
			if u.Kind == "movie" { // (a show's episodes say so as they go)
				s.printf("= %s: %s is already in the library (%s); left in import\n", u.Label, u.Title, s.rel(u.Dest))
			}
		case UnitFailed:
			s.printf("! %s: %s\n", u.Label, u.Err)
		}
	}
	for _, v := range r.Unknown {
		s.printf("? %s: can't tell what this is; add a .mvd hint (kind = \"movie\" or \"tv\", tmdb = ...) and import again\n", s.rel(v))
	}
	if len(r.Unfiled) > 0 {
		s.printf("  %s: %d video(s) not filed, so the rest is left where it is\n", r.Label, len(r.Unfiled))
	}
}
