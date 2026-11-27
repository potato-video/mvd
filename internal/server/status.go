package server

import (
	"bufio"
	"bytes"
	"math"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"mvd/internal/library"
)

// GET /api/status: how the server is doing, for `mvd status` and the logo's
// tooltip. Cheap enough to poll: one ReadMemStats (a brief pause), a read
// of /proc/self/status and a statfs of the library's drive.
type statusView struct {
	Version    string        `json:"version"`
	API        int           `json:"api"` // APILevel
	Go         string        `json:"go"`
	Platform   string        `json:"platform"` // "linux/mips"
	Started    time.Time     `json:"started"`
	Uptime     int64         `json:"uptime"` // seconds
	Memory     memoryView    `json:"memory"`
	Goroutines int           `json:"goroutines"`
	Library    libraryCounts `json:"library"`
	Free       *int64        `json:"free,omitempty"` // bytes free on the library's drive, where that's known
	Uploads    int           `json:"uploads"`        // under way: started and not yet finished
	CPU        *cpuView      `json:"cpu,omitempty"`  // Linux only
	Access     accessView    `json:"access"`         // the token the request came with
	Features   []string      `json:"features"`       // what it can do now (features)
}

// features is what the server can do now, beyond the library: each only
// when it's built in and set up, so a client offers what will work. A
// list rather than an object, so a part can be added, or split, without
// tying the API to today's shape. The routes are still the admin's.
//
//	downloads  /api/downloads: the downloads feature, and Transmission's address
//	needs-you  Needs you (/api/import): the files feature, and an import folder
//	trash      /api/trash: the files feature, and the trash on (leftovers go there, not deleted)
//	uploads    /api/uploads: the files feature, an import folder, and [server] uploads = true
func (sv *Server) features() []string {
	cfg := sv.cfg
	f := []string{}
	if sv.hasDownloads() && cfg.Transmission != "" {
		f = append(f, "downloads")
	}
	if uploadsBuilt && cfg.ImportDir != "" {
		f = append(f, "needs-you")
	}
	if uploadsBuilt && cfg.TrashDir != "" && cfg.TrashMaxAge > 0 && cfg.TrashMaxSize > 0 {
		f = append(f, "trash")
	}
	if uploadsBuilt && cfg.Uploads && cfg.ImportDir != "" {
		f = append(f, "uploads")
	}
	return f
}

// accessView says which token a request has: its name ("admin" for the
// [server] token, "" with no token at all), and whether it's the admin's.
type accessView struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// cpuView: Percent is this process's share of one core over the last 10
// seconds (more than 100 on several cores), omitted until there are two
// samples; Load is the system's 1, 5 and 15 minute load averages.
type cpuView struct {
	Percent *float64   `json:"percent,omitempty"`
	Seconds float64    `json:"seconds"` // CPU time used since it started
	Load    [3]float64 `json:"load"`
	Cores   int        `json:"cores"`
}

// memoryView is in bytes. RSS is what the system sees (what top and free
// count); Go is what the Go runtime has taken from the system, of which
// Heap is live data.
type memoryView struct {
	RSS     uint64 `json:"rss,omitempty"`      // now (Linux only)
	PeakRSS uint64 `json:"peak_rss,omitempty"` // since it started (Linux only)
	Go      uint64 `json:"go"`
	Heap    uint64 `json:"heap"`
	GCs     uint32 `json:"gcs"`
}

type libraryCounts struct {
	Movies   int   `json:"movies"`
	Shows    int   `json:"shows"`
	Episodes int   `json:"episodes"`
	Bytes    int64 `json:"bytes"` // the videos' size
}

func (sv *Server) status() statusView {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := statusView{Version: Version, API: APILevel, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Started: sv.started, Uptime: int64(sv.clock().Sub(sv.started) / time.Second),
		Memory:     memoryView{Go: ms.Sys, Heap: ms.HeapAlloc, GCs: ms.NumGC},
		Goroutines: runtime.NumGoroutine(), Features: sv.features()}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		st.Memory.RSS, st.Memory.PeakRSS = readProcStatus(b)
	}
	if ix := sv.ix.Load(); ix != nil {
		st.Library = libraryCounts{len(ix.Movies), len(ix.Shows), ix.EpisodeCount(), ix.Bytes()}
	}
	st.Uploads = sv.uploadsGoing()
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		if ticks, ok := readProcStat(b); ok {
			st.CPU = &cpuView{Seconds: float64(ticks) / clockTicks, Cores: runtime.NumCPU()}
			if p, ok := sv.cpu.percent(); ok {
				st.CPU.Percent = &p
			}
			if b, err := os.ReadFile("/proc/loadavg"); err == nil {
				st.CPU.Load, _ = readLoadAvg(b)
			}
		}
	}
	return st
}

// clockTicks is how many ticks /proc counts CPU time in per second: USER_HZ,
// 100 on every Linux (sysconf would need cgo).
const clockTicks = 100

// cpuSampler keeps the last few readings of the CPU time used, taken every
// cpuEvery, so the status can say how busy it's been lately.
type cpuSampler struct {
	mu      sync.Mutex
	samples []cpuSample // oldest first, at most cpuSamples
}

type cpuSample struct {
	at    time.Time
	ticks uint64
}

const (
	cpuEvery   = 5 * time.Second
	cpuSamples = 3 // so the share is over the last 10 seconds
)

func (c *cpuSampler) add(at time.Time, ticks uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, cpuSample{at, ticks})
	if len(c.samples) > cpuSamples {
		c.samples = c.samples[len(c.samples)-cpuSamples:]
	}
}

// percent is the share of one core between the oldest and newest samples,
// to a tenth.
func (c *cpuSampler) percent() (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) < 2 {
		return 0, false
	}
	a, b := c.samples[0], c.samples[len(c.samples)-1]
	secs := b.at.Sub(a.at).Seconds()
	if secs <= 0 || b.ticks < a.ticks {
		return 0, false
	}
	p := float64(b.ticks-a.ticks) / clockTicks / secs * 100
	return math.Round(p*10) / 10, true
}

// sampleCPU takes one reading; false where there's no /proc.
func (sv *Server) sampleCPU(at time.Time) bool {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return false
	}
	ticks, ok := readProcStat(b)
	if ok {
		sv.cpu.add(at, ticks)
	}
	return ok
}

// sampleCPUEvery samples until stop closes. Not at all without /proc.
func (sv *Server) sampleCPUEvery(stop <-chan struct{}) {
	if !sv.sampleCPU(sv.clock()) {
		return
	}
	t := time.NewTicker(cpuEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			sv.sampleCPU(sv.clock())
		}
	}
}

// readProcStat takes utime + stime, in ticks, from /proc/self/stat. The
// fields are counted after the name, "(mvd)", which could hold spaces or
// brackets, so after its last ")".
func readProcStat(b []byte) (uint64, bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b[i+1:])) // f[0] is field 3, the state
	if len(f) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(f[11], 10, 64)
	stime, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

// readLoadAvg takes the three load averages from /proc/loadavg.
func readLoadAvg(b []byte) (l [3]float64, ok bool) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return l, false
	}
	for i := range l {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return l, false
		}
		l[i] = v
	}
	return l, true
}

// readProcStatus takes VmRSS and VmHWM (the peak) from /proc/self/status,
// in bytes.
func readProcStatus(b []byte) (rss, peak uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || (k != "VmRSS" && k != "VmHWM") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
		if err != nil {
			continue
		}
		if k == "VmRSS" {
			rss = n << 10
		} else {
			peak = n << 10
		}
	}
	return rss, peak
}

// freeSpace is how much can still be written to the library's drive: the
// movies folder's, or the TV folder's without one.
func (sv *Server) freeSpace() (int64, bool) {
	dir := sv.cfg.MoviesDir
	if dir == "" {
		dir = sv.cfg.TVDir
	}
	if dir == "" {
		return 0, false
	}
	return library.FreeSpace(dir)
}

func (sv *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	st := sv.status()
	s := sv.shelf(r)
	st.Access = accessView{Name: s.acc.name, Admin: s.acc.admin}
	if s.ix != nil {
		st.Library = s.counts() // only what this token can see
	}
	if free, ok := sv.freeSpace(); ok {
		st.Free = &free
	}
	writeJSON(w, st)
}
