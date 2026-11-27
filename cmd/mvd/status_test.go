package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mvd/internal/config"
)

const statusJSON = `{"version":"99bb558","go":"go1.24.7","platform":"linux/mips",
	"started":"2026-10-30T15:56:00Z","uptime":11580,
	"memory":{"rss":16330752,"peak_rss":18006016,"go":9541632,"heap":4404019,"gcs":12},
	"goroutines":13,"library":{"movies":312,"shows":41,"episodes":1890,"bytes":1319413953331},
	"free":3738339534438,"uploads":1,
	"cpu":{"percent":3.2,"seconds":252.4,"load":[0.42,0.31,0.25],"cores":1},
	"features":["downloads","needs-you","trash","uploads"]}`

func fakeStatus(t *testing.T) *apiClient {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, statusJSON)
	}))
	t.Cleanup(srv.Close)
	return &apiClient{base: srv.URL, http: srv.Client()}
}

func TestStatusReport(t *testing.T) {
	st, err := getStatus(fakeStatus(t))
	if err != nil {
		t.Fatal(err)
	}
	got := statusReport(st)
	for _, want := range []string{
		"mvd 99bb558 on linux/mips (go1.24.7), up 3h 13m",
		"memory   15.6 MB in use, 17.2 MB at most; Go has 9.1 MB, 4.2 MB of it live; 12 GCs",
		"library  312 movies, 41 shows, 1890 episodes; 1.2 TB, 3.4 TB free",
		"busy     13 goroutines, 1 upload under way",
		"cpu      3.2% now; 4m 12s since it started; load 0.42 0.31 0.25",
		"offers   downloads, needs-you, trash, uploads",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// -every prints one line at a time.
	if line := statusLine(st); !strings.Contains(line, "  cpu 3.2%  15.6 MB (peak 17.2 MB)  heap 4.2 MB  13 goroutines  1 upload") {
		t.Errorf("line: %q", line)
	}
}

// Without /proc (a Mac), there's no rss; say what there is.
func TestStatusReportNoRSS(t *testing.T) {
	st := serverStatus{Version: "dev", Platform: "darwin/arm64", Go: "go1.24.7", Uptime: 40}
	st.Memory.Go, st.Memory.Heap = 9541632, 4404019
	got := statusReport(st)
	if !strings.Contains(got, "memory   Go has 9.1 MB, 4.2 MB of it live; 0 GCs\n") ||
		!strings.Contains(got, "up under a minute") || !strings.Contains(got, "no uploads") || strings.Contains(got, "cpu") {
		t.Errorf("%s", got)
	}
}

// What a server can do beyond the library; an older server doesn't say.
func TestStatusFeaturesLine(t *testing.T) {
	var st serverStatus
	if got := statusReport(st); strings.Contains(got, "offers") {
		t.Errorf("an older server: %s", got)
	}
	st.Features = []string{}
	if got := statusReport(st); !strings.Contains(got, "offers   the library only\n") {
		t.Errorf("nothing more: %s", got)
	}
}

// Before its second sample the server has no share to give; and on a box
// with several cores, the share is of one of them.
func TestStatusCPULine(t *testing.T) {
	var st serverStatus
	st.CPU = &cpuStatus{Seconds: 12.34, Load: [3]float64{1.5, 1, 0.75}, Cores: 4}
	if got := statusReport(st); !strings.Contains(got, "cpu      12.3s since it started; load 1.50 1.00 0.75 on 4 cores\n") {
		t.Errorf("%s", got)
	}
	if got := statusLine(st); strings.Contains(got, "cpu") {
		t.Errorf("line without a share: %q", got)
	}
	for secs, want := range map[float64]string{0.4: "0.4s", 59.94: "59.9s", 252.4: "4m 12s", 3725: "1h 2m"} {
		if got := cpuTime(secs); got != want {
			t.Errorf("cpuTime(%v) = %q, want %q", secs, got, want)
		}
	}
}

func TestUpFor(t *testing.T) {
	for secs, want := range map[int64]string{30: "under a minute", 300: "5m", 7200: "2h", 11580: "3h 13m",
		3*86400 + 4*3600 + 59: "3d 4h", 86400: "1d"} {
		if got := upFor(secs); got != want {
			t.Errorf("%d: %q, want %q", secs, got, want)
		}
	}
}

// A short token is warned about, as the server does when it starts.
func TestStatusTokenWarnings(t *testing.T) {
	if got := tokenWarnings(config.Config{Token: "0123456789abcdef"}); got != "" {
		t.Errorf("long enough: %q", got)
	}
	got := tokenWarnings(config.Config{Token: "0123456789abcdef",
		Tokens: []config.Token{{Name: "kids", Token: "short"}}})
	if !strings.HasPrefix(got, "warning  [[server.tokens]] \"kids\": the token is under 16 characters") {
		t.Errorf("%q", got)
	}
}
