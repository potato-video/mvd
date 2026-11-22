//go:build webui && files

package server

import (
	"strings"
	"testing"
	"time"
)

func TestIncomingWords(t *testing.T) {
	for sec, want := range map[float64]string{1: "1 second remaining", 23: "23 seconds remaining",
		720: "12 minutes remaining", 7200: "2 hours remaining", 86400 * 3: "3 days remaining"} {
		if got := remaining(sec); got != want {
			t.Errorf("remaining(%v) = %q, want %q", sec, got, want)
		}
	}
	for bps, want := range map[float64]string{18 << 10: "18 kB/sec", 2.1 * (1 << 20): "2.1 MB/sec", 500: "500 bytes/sec"} {
		if got := rateWords(bps); got != want {
			t.Errorf("rateWords(%v) = %q, want %q", bps, got, want)
		}
	}
}

// A folder being uploaded is one row, however many of its files are going.
func TestIncomingFolderUpload(t *testing.T) {
	ts, _ := newImportServer(t)
	ts.startUpload(`{"name": "Season 2/S02E01.mkv", "size": 1000, "group": "0123456789abcdef", "group_files": 3}`, 201)
	ts.startUpload(`{"name": "Season 2/S02E02.mkv", "size": 1000, "group": "0123456789abcdef", "group_files": 3}`, 201)
	v := ts.sv.incoming()
	if len(v.Rows) != 1 {
		t.Fatalf("rows: %+v", v.Rows)
	}
	if r := v.Rows[0]; r.Name != "Season 2" || r.Upload != "0123456789abcdef" ||
		!strings.HasPrefix(r.Line, "0 of 3 files from another computer · ") {
		t.Errorf("folder: %+v", r)
	}
}

// A missing import folder (the drive unplugged) is said once, above the list.
func TestIncomingNoDrive(t *testing.T) {
	ts, _ := newImportServer(t)
	ts.sv.cfg.ImportDir = "/nowhere/import"
	v := ts.sv.incoming()
	if len(v.Banners) != 1 || !strings.Contains(v.Banners[0], "is the drive plugged in?") || v.Free != "" {
		t.Errorf("banners: %q", v.Banners)
	}
}

// An upload's line: how much of it is in, and how long to go, no speed.
func TestIncomingUploadLine(t *testing.T) {
	ts, _ := newImportServer(t)
	u := ts.startUpload(`{"name": "Heat.1995.mkv", "size": 3000000}`, 201)
	ts.sv.incoming() // the server sees it going from nothing...
	ts.put(u.ID, 0, make([]byte, 1<<20))
	start := time.Now()
	ts.sv.now = func() time.Time { return start.Add(10 * time.Second) } // ...and 1 MB in 10 seconds later
	if r := ts.sv.incoming().Rows[0]; r.Line != "1 MB of 3 MB from another computer · 19 seconds remaining" {
		t.Errorf("line: %q", r.Line)
	}
	if got := sizeOf(0, 5<<40); got != "5.0 TB" {
		t.Errorf("sizeOf: %q", got)
	}
}
