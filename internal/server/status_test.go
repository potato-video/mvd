package server

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// /api/status says how the server is: its build, how long it's been up,
// its memory, the library's size, the free space on its drive and uploads
// under way.
func TestStatus(t *testing.T) {
	ts, _ := uploadServer(t)
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := start
	ts.sv.now = func() time.Time { return now }
	ts.sv.started = start
	now = start.Add(90 * time.Minute)
	wantUploads := 0
	if uploadsBuilt {
		ts.do("POST", "/api/uploads", `{"name": "Movie (2001).mkv", "size": 10}`, nil).Body.Close()
		wantUploads = 1
	}

	var st statusView
	ts.json("GET", "/api/status", "", &st)
	ix := ts.sv.ix.Load()
	switch {
	case st.Version != Version || st.API != APILevel || st.Go != runtime.Version() || st.Platform != runtime.GOOS+"/"+runtime.GOARCH:
		t.Errorf("build: %+v", st)
	case !st.Started.Equal(start) || st.Uptime != 5400:
		t.Errorf("started %v, uptime %d", st.Started, st.Uptime)
	case st.Memory.Heap == 0 || st.Memory.Go < st.Memory.Heap || st.Goroutines == 0:
		t.Errorf("memory: %+v, goroutines %d", st.Memory, st.Goroutines)
	case st.Library.Movies != len(ix.Movies) || st.Library.Shows != len(ix.Shows) ||
		st.Library.Episodes != ix.EpisodeCount() || st.Library.Episodes == 0 ||
		st.Library.Bytes != ix.Bytes() || st.Library.Bytes == 0:
		t.Errorf("library: %+v", st.Library)
	case runtime.GOOS != "windows" && (st.Free == nil || *st.Free <= 0):
		t.Errorf("free space: %v", st.Free)
	case st.Uploads != wantUploads:
		t.Errorf("uploads: %d, want %d", st.Uploads, wantUploads)
	}
	// What the system sees, where there's /proc to ask (the router, Linux).
	if _, err := os.Stat("/proc/self/status"); err == nil && (st.Memory.RSS == 0 || st.Memory.PeakRSS < st.Memory.RSS) {
		t.Errorf("rss: %+v", st.Memory)
	}
}

// Every response says the API's level, beside the build, refusals too.
func TestAPILevelHeader(t *testing.T) {
	ts := newTestServer(t, "secret")
	defer ts.hs.Close()
	for _, path := range []string{"/api/status", "/api/movies"} {
		r := ts.do("GET", path, "", nil)
		r.Body.Close()
		if r.Header.Get("X-Mvd-Api") != strconv.Itoa(APILevel) || r.Header.Get("X-Mvd-Version") != Version {
			t.Errorf("%s (%d): %v", path, r.StatusCode, r.Header)
		}
	}
}

// Uploads turned off: none counted, and no error.
func TestStatusNoUploads(t *testing.T) {
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	var st statusView
	ts.json("GET", "/api/status", "", &st)
	if st.Uploads != 0 || st.Library.Movies == 0 {
		t.Errorf("%+v", st)
	}
}

// Like the rest of the API, it wants the token when there is one.
func TestStatusNeedsToken(t *testing.T) {
	ts := newTestServer(t, "secret")
	defer ts.hs.Close()
	if r := ts.do("GET", "/api/status", "", nil); r.StatusCode != 401 {
		t.Errorf("no token: HTTP %d", r.StatusCode)
	}
	if r := ts.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer secret"}); r.StatusCode != 200 {
		t.Errorf("token: HTTP %d", r.StatusCode)
	}
}

func TestReadProcStatus(t *testing.T) {
	rss, peak := readProcStatus([]byte("Name:\tmvd\nVmHWM:\t   31024 kB\nVmRSS:\t   23412 kB\nThreads:\t6\n"))
	if rss != 23412<<10 || peak != 31024<<10 {
		t.Errorf("rss %d, peak %d", rss, peak)
	}
	if rss, peak := readProcStatus(nil); rss != 0 || peak != 0 {
		t.Errorf("empty: %d %d", rss, peak)
	}
}

// CPU time from /proc/self/stat: utime and stime, the 14th and 15th fields,
// counted after the name, which can hold spaces and brackets.
func TestReadProcStat(t *testing.T) {
	line := "7436 (mvd (a) b) S 1 7436 7436 0 -1 4194560 3170 0 3 0 1500 250 0 0 20 0 13 0 2900 590348288 3987\n"
	if ticks, ok := readProcStat([]byte(line)); !ok || ticks != 1750 {
		t.Errorf("ticks %d, %v; want 1750", ticks, ok)
	}
	for _, bad := range []string{"", "7436 (mvd S 1", "7436 (mvd) S 1 2"} {
		if _, ok := readProcStat([]byte(bad)); ok {
			t.Errorf("%q: ok", bad)
		}
	}
}

func TestReadLoadAvg(t *testing.T) {
	if l, ok := readLoadAvg([]byte("0.22 0.26 0.30 1/105 20119\n")); !ok || l != [3]float64{0.22, 0.26, 0.30} {
		t.Errorf("%v %v", l, ok)
	}
	if _, ok := readLoadAvg([]byte("x")); ok {
		t.Error("x: ok")
	}
}

// The CPU's share over the last samples, taken every 5 seconds: a tick is
// a hundredth of a second, so 250 ticks over 10 seconds is 25%.
func TestCPUSampler(t *testing.T) {
	var c cpuSampler
	t0 := time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC)
	if _, ok := c.percent(); ok {
		t.Error("no samples: ok")
	}
	c.add(t0, 1000)
	if _, ok := c.percent(); ok {
		t.Error("one sample: ok")
	}
	c.add(t0.Add(5*time.Second), 1100)
	if p, ok := c.percent(); !ok || p != 20 {
		t.Errorf("two samples: %v %v, want 20", p, ok)
	}
	c.add(t0.Add(10*time.Second), 1250)
	if p, _ := c.percent(); p != 25 {
		t.Errorf("three: %v, want 25 (over 10 seconds)", p)
	}
	// The oldest drops out: it's always the last 10 seconds.
	c.add(t0.Add(15*time.Second), 1290)
	if p, _ := c.percent(); p != 19 {
		t.Errorf("four: %v, want 19 (1290-1100 over 10 seconds)", p)
	}
}

// Where there's /proc, the status has the CPU time used and the load.
func TestStatusCPU(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	ts := newTestServer(t, "")
	defer ts.hs.Close()
	now := time.Now()
	ts.sv.cpu.add(now.Add(-10*time.Second), 0)
	ts.sv.sampleCPU(now)
	var st statusView
	ts.json("GET", "/api/status", "", &st)
	if st.CPU == nil || st.CPU.Seconds <= 0 || st.CPU.Percent == nil || st.CPU.Cores != runtime.NumCPU() {
		t.Errorf("cpu: %+v", st.CPU)
	}
}
