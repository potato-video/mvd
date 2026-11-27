package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"mvd/internal/config"
)

// mvd status: how the server is doing, from GET /api/status.
type serverStatus struct {
	Version  string `json:"version"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
	Uptime   int64  `json:"uptime"`
	Memory   struct {
		RSS     int64  `json:"rss"`
		PeakRSS int64  `json:"peak_rss"`
		Go      int64  `json:"go"`
		Heap    int64  `json:"heap"`
		GCs     uint32 `json:"gcs"`
	} `json:"memory"`
	Goroutines int `json:"goroutines"`
	Library    struct {
		Movies   int   `json:"movies"`
		Shows    int   `json:"shows"`
		Episodes int   `json:"episodes"`
		Bytes    int64 `json:"bytes"` // 0 from servers before it was there
	} `json:"library"`
	Free     *int64     `json:"free"` // nil where the server can't tell
	Uploads  int        `json:"uploads"`
	CPU      *cpuStatus `json:"cpu"`      // nil without /proc (a Mac)
	Features []string   `json:"features"` // nil from servers before API level 2
}

type cpuStatus struct {
	Percent *float64   `json:"percent"` // nil for the server's first 5 seconds
	Seconds float64    `json:"seconds"`
	Load    [3]float64 `json:"load"`
	Cores   int        `json:"cores"`
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the server's JSON as it is")
	every := fs.Duration("every", 0, "print a line every so often (5s, say) until stopped")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mvd status [-json] [-every 5s]\n\nHow the server is doing: its build, uptime, memory, library and uploads, and what it offers beyond the library.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	cfg := config.Load()
	api, ok := clientAPI(cfg)
	if !ok {
		return 1
	}
	api.hint = ""

	if *asJSON {
		var raw json.RawMessage
		if err := api.get("/api/status", &raw); err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 1
		}
		fmt.Println(string(raw))
		return 0
	}
	st, err := getStatus(api)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if *every <= 0 {
		fmt.Print(statusReport(st))
		fmt.Print(tokenWarnings(cfg))
		return 0
	}
	for {
		fmt.Println(statusLine(st))
		time.Sleep(*every)
		if st, err = getStatus(api); err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 1
		}
	}
}

func getStatus(api *apiClient) (serverStatus, error) {
	var st serverStatus
	err := api.get("/api/status", &st)
	return st, err
}

// statusReport is the full picture, a few lines.
func statusReport(st serverStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mvd %s on %s (%s), up %s\n", st.Version, st.Platform, st.Go, upFor(st.Uptime))
	m := st.Memory
	b.WriteString("memory   ")
	if m.RSS > 0 {
		fmt.Fprintf(&b, "%s in use, %s at most; ", mb(m.RSS), mb(m.PeakRSS))
	}
	fmt.Fprintf(&b, "Go has %s, %s of it live; %d GCs\n", mb(m.Go), mb(m.Heap), m.GCs)
	fmt.Fprintf(&b, "library  %d movies, %d shows, %d episodes", st.Library.Movies, st.Library.Shows, st.Library.Episodes)
	if st.Library.Bytes > 0 {
		fmt.Fprintf(&b, "; %s", sizeStr(st.Library.Bytes))
		if st.Free != nil {
			fmt.Fprintf(&b, ", %s free", sizeStr(*st.Free))
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "busy     %d goroutines, %s\n", st.Goroutines, uploadsStr(st.Uploads, "no uploads", " under way"))
	if c := st.CPU; c != nil {
		b.WriteString("cpu      ")
		if c.Percent != nil {
			fmt.Fprintf(&b, "%.1f%% now; ", *c.Percent)
		}
		fmt.Fprintf(&b, "%s since it started; load %.2f %.2f %.2f", cpuTime(c.Seconds), c.Load[0], c.Load[1], c.Load[2])
		if c.Cores > 1 {
			fmt.Fprintf(&b, " on %d cores", c.Cores)
		}
		b.WriteString("\n")
	}
	if st.Features != nil {
		b.WriteString("offers   ")
		if len(st.Features) == 0 {
			b.WriteString("the library only")
		}
		b.WriteString(strings.Join(st.Features, ", ") + "\n")
	}
	return b.String()
}

// tokenWarnings are the lines saying which tokens in config.toml are too
// short, as the server warns when it starts.
func tokenWarnings(cfg config.Config) string {
	w, _ := cfg.CheckTokens()
	var b strings.Builder
	for _, s := range w {
		fmt.Fprintf(&b, "warning  %s\n", s)
	}
	return b.String()
}

// sizeStr is a size for people: "812 KB", "4 MB", "1.2 GB", "3.4 TB".
func sizeStr(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

// cpuTime is CPU time used: "0.4s", "4m 12s", "1h 2m".
func cpuTime(secs float64) string {
	switch s := int(secs); {
	case secs < 60:
		return fmt.Sprintf("%.1fs", math.Floor(secs*10)/10)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
}

// statusLine is one line, for -every.
func statusLine(st serverStatus) string {
	m := st.Memory
	mem := mb(m.Go) + " Go"
	if m.RSS > 0 {
		mem = fmt.Sprintf("%s (peak %s)", mb(m.RSS), mb(m.PeakRSS))
	}
	cpu := ""
	if st.CPU != nil && st.CPU.Percent != nil {
		cpu = fmt.Sprintf("  cpu %.1f%%", *st.CPU.Percent)
	}
	return fmt.Sprintf("%s%s  %s  heap %s  %d goroutines  %s", time.Now().Format("15:04:05"), cpu, mem, mb(m.Heap),
		st.Goroutines, uploadsStr(st.Uploads, "no uploads", ""))
}

func uploadsStr(n int, none, suffix string) string {
	switch n {
	case 0:
		return none
	case 1:
		return "1 upload" + suffix
	}
	return fmt.Sprintf("%d uploads%s", n, suffix)
}

// mb is a memory size in MB (binary, as top and free count), to a tenth.
func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// upFor is how long it's been up: "5m", "3h 13m", "3d 4h".
func upFor(secs int64) string {
	d := time.Duration(secs) * time.Second
	if d < 24*time.Hour {
		return short(d)
	}
	days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

// short is how long, roughly: "under a minute", "5m", "1h 20m".
func short(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 1:
		return "under a minute"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}
