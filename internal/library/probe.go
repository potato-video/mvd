package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What's inside a video file, from ffprobe when it's on the PATH: the
// container, the duration and each track's codec, language, title and
// flags. Only the file's own tags, no guessing. Without ffprobe nothing is
// probed and clients do without.

// MediaFile is one probed file. Size and ModTime are the file's when it was
// probed, so a rescan keeps the result while the file is unchanged. A file
// ffprobe couldn't read has no Container, and isn't tried again until it
// changes.
type MediaFile struct {
	V         int       `json:"v,omitempty"` // probeVersion when probed
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mtime"`
	Container string    `json:"container,omitempty"`
	Duration  float64   `json:"duration,omitempty"` // seconds
	Tracks    []Track   `json:"tracks,omitempty"`
}

// OK says ffprobe read the file.
func (mf *MediaFile) OK() bool { return mf != nil && mf.Container != "" }

// Track is one video, audio or subtitle stream, in the file's order.
type Track struct {
	Kind            string `json:"kind"` // video, audio or subtitle
	Codec           string `json:"codec,omitempty"`
	Lang            string `json:"lang,omitempty"`
	Title           string `json:"title,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearing_impaired,omitempty"`
	Commentary      bool   `json:"commentary,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
}

var (
	ffprobeOnce sync.Once
	ffprobePath string
	probeMu     sync.Mutex // one ffprobe at a time
)

// FFprobe is ffprobe's path, "" when it isn't on the PATH. Looked up once.
func FFprobe() string {
	ffprobeOnce.Do(func() { ffprobePath, _ = exec.LookPath("ffprobe") })
	return ffprobePath
}

const (
	probeTimeout = 30 * time.Second // a broken file can't stall a scan
	probeMaxOut  = 256 << 10        // 43 tracks are about 10 KB of JSON, 4 KB described
)

// probeVersion changes when probing gets better, so files are probed
// again. 2: reading ffprobe's description when its JSON stops short.
const probeVersion = 2

var errNoFFprobe = errors.New("ffprobe isn't installed")

// Probe asks ffprobe about a file. A file ffprobe can't read gives a
// MediaFile without a Container and no error; the error is for when there's
// no ffprobe, or no file.
func Probe(path string) (*MediaFile, error) {
	ff := FFprobe()
	if ff == "" {
		return nil, errNoFFprobe
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	mf := &MediaFile{V: probeVersion, Size: st.Size(), ModTime: st.ModTime()}
	probeMu.Lock()
	stdout, stderr := runFFprobe(ff, path)
	probeMu.Unlock()
	readProbe(mf, stdout, stderr, path)
	return mf, nil
}

// readProbe fills in mf from ffprobe's JSON, or its description of the file
// when the JSON stops short.
func readProbe(mf *MediaFile, stdout, stderr []byte, path string) {
	if !parseProbe(mf, stdout, path) {
		parseDump(mf, stderr, path)
	}
}

// runFFprobe returns what ffprobe printed: the JSON on stdout and its
// description of the file on stderr, each at most probeMaxOut. The exit
// status is ignored. ffprobe opens a decoder for each stream after reading
// the headers, and where one won't open (the router's only HEVC decoder is
// hardware it doesn't have) it stops there, having printed the description
// but no more JSON than "{".
func runFFprobe(ff, path string) (stdout, stderr []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-v", "info", "-hide_banner",
		// Only the headers: they have everything wanted, and stream info
		// otherwise reads well into the file trying to open decoders.
		"-probesize", "1000000", "-analyzeduration", "1000000",
		// Local files only: a "video" that's really a playlist (HLS,
		// ffconcat) mustn't have ffprobe fetch what it names. Recent
		// FFmpeg won't take HLS without its extension; older may.
		"-protocol_whitelist", "file",
		"-show_entries", "format=format_name,duration"+
			":stream=index,codec_type,codec_name,channels,width,height"+
			":stream_tags=language,title"+
			":stream_disposition=default,forced,hearing_impaired,comment,attached_pic",
		"-of", "json=c=1", "--", path)
	out, errOut := &capped{max: probeMaxOut}, &capped{max: probeMaxOut}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.Run()
	return out.b, errOut.b
}

// capped keeps the first max bytes written to it, and drops the rest.
type capped struct {
	b   []byte
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

type probeJSON struct {
	Streams []struct {
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		Channels    int               `json:"channels"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// parseProbe fills in mf from ffprobe's JSON, and says whether there was
// any to read: when ffprobe stops early, there isn't.
func parseProbe(mf *MediaFile, b []byte, path string) bool {
	var p probeJSON
	if len(b) == 0 || json.Unmarshal(b, &p) != nil || p.Format.FormatName == "" {
		return false
	}
	mf.Container = containerName(p.Format.FormatName, path)
	mf.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video", "audio", "subtitle":
		default:
			continue // attachments (fonts), data
		}
		if s.Disposition["attached_pic"] != 0 {
			continue // cover art
		}
		t := Track{Kind: s.CodecType, Codec: s.CodecName, Title: tag(s.Tags, "title"),
			Default: s.Disposition["default"] != 0, Forced: s.Disposition["forced"] != 0,
			HearingImpaired: s.Disposition["hearing_impaired"] != 0, Commentary: s.Disposition["comment"] != 0}
		if l := strings.ToLower(tag(s.Tags, "language")); l != "und" {
			t.Lang = l
		}
		switch s.CodecType {
		case "audio":
			t.Channels = s.Channels
		case "video":
			t.Width, t.Height = s.Width, s.Height
		}
		mf.Tracks = append(mf.Tracks, t)
	}
	return true
}

// tag reads a stream tag. Tags' case varies by container (MP4 and AVI
// writers aren't consistent), so any case will do.
func tag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return strings.TrimSpace(v)
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// containerName simplifies ffprobe's format_name, a list of the names its
// demuxer goes by ("matroska,webm", "mov,mp4,m4a,3gp,3g2,mj2"), to the one
// people know the file as.
func containerName(format, path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	first, _, _ := strings.Cut(format, ",")
	switch first {
	case "matroska":
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case "mov":
		if ext == "mov" || ext == "m4v" {
			return ext
		}
		return "mp4"
	case "mpegts":
		return "ts"
	case "mpeg":
		return "mpg"
	case "asf":
		return "wmv"
	}
	return first
}

// Probed is what's been probed so far, by path, for BuildIndex to carry over.
type Probed map[string]*MediaFile

// still returns the probe of path when the file hasn't changed since; st is
// the file's stat.
func (p Probed) still(path string, st os.FileInfo) *MediaFile {
	mf := p[path]
	if mf == nil || st == nil || mf.V != probeVersion || mf.Size != st.Size() || !mf.ModTime.Equal(st.ModTime()) {
		return nil
	}
	return mf
}

// Probed lists the index's probed files.
func (ix *Index) Probed() Probed {
	out := Probed{}
	for _, m := range ix.Movies {
		for i, mf := range m.Media {
			if mf != nil && i < len(m.Files) {
				out[m.Files[i]] = mf
			}
		}
	}
	for _, e := range ix.ep {
		if e.Media != nil {
			out[e.File] = e.Media
		}
	}
	return out
}

// Unprobed lists the index's video files that haven't been probed, movies
// first.
func (ix *Index) Unprobed() []string {
	var out []string
	for _, m := range ix.Movies {
		for i, f := range m.Files {
			if i >= len(m.Media) || m.Media[i] == nil {
				out = append(out, f)
			}
		}
	}
	for _, sh := range ix.Shows {
		for _, se := range sh.Seasons {
			for _, e := range se.Episodes {
				if e.Media == nil {
					out = append(out, e.File)
				}
			}
		}
	}
	return out
}

// WithMedia returns a copy of the index with the probes in p added, leaving
// ix as it was (the server's handlers may be reading it). Only the movies
// and shows that change are copied.
func (ix *Index) WithMedia(p Probed) *Index {
	nx := &Index{Built: ix.Built, bytes: ix.bytes, movie: make(map[string]*Movie, len(ix.movie)),
		show: make(map[string]*Show, len(ix.show)), ep: make(map[string]*Episode, len(ix.ep))}
	for _, m := range ix.Movies {
		if changed := movieMedia(m, p); changed != nil {
			c := *m
			c.Media = changed
			m = &c
		}
		nx.Movies = append(nx.Movies, m)
		nx.movie[m.ID] = m
	}
	for _, sh := range ix.Shows {
		if showGains(sh, p) {
			sh = copyShow(sh, p)
		}
		nx.Shows = append(nx.Shows, sh)
		nx.show[sh.ID] = sh
		for _, se := range sh.Seasons {
			for _, e := range se.Episodes {
				nx.ep[e.ID] = e
			}
		}
	}
	return nx
}

// movieMedia is m's Media with p's probes added, nil if p adds none.
func movieMedia(m *Movie, p Probed) []*MediaFile {
	var out []*MediaFile
	for i, f := range m.Files {
		if mf := p[f]; mf != nil && (i >= len(m.Media) || m.Media[i] != mf) {
			if out == nil {
				out = make([]*MediaFile, len(m.Files))
				copy(out, m.Media)
			}
			out[i] = mf
		}
	}
	return out
}

func showGains(sh *Show, p Probed) bool {
	for _, se := range sh.Seasons {
		for _, e := range se.Episodes {
			if mf := p[e.File]; mf != nil && e.Media != mf {
				return true
			}
		}
	}
	return false
}

// copyShow copies a show with its seasons and episodes (each episode points
// back at its show), adding p's probes.
func copyShow(sh *Show, p Probed) *Show {
	c := *sh
	c.Seasons = make([]*Season, len(sh.Seasons))
	for i, se := range sh.Seasons {
		cs := *se
		cs.Episodes = make([]*Episode, len(se.Episodes))
		for j, e := range se.Episodes {
			ce := *e
			ce.Show = &c
			if mf := p[e.File]; mf != nil {
				ce.Media = mf
			}
			cs.Episodes[j] = &ce
		}
		c.Seasons[i] = &cs
	}
	return &c
}

// parseDump fills in mf from ffprobe's description of the file (as
// av_dump_format prints it), for when its JSON stops short:
//
//	Input #0, matroska,webm, from 'a.mkv':
//	  Duration: 00:59:52.45, start: 0.000000, bitrate: 2878 kb/s
//	  Stream #0:1[0x2](eng): Audio: eac3, 48000 Hz, 5.1(side), fltp, 768 kb/s (default)
//	    Metadata:
//	      title           : Director's commentary
//
// The same fields as the JSON gives, with the duration to a hundredth of a
// second. Leaves mf without a Container when there's no description.
func parseDump(mf *MediaFile, b []byte, path string) {
	type stream struct {
		n      int
		t      Track
		indent int
	}
	var streams []*stream
	var cur *stream
	seen := map[int]bool{} // MPEG-TS lists a stream under each of its programs
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "Input #0, "):
			format, _, ok := strings.Cut(strings.TrimPrefix(trimmed, "Input #0, "), ", from '")
			if ok {
				mf.Container = containerName(format, path)
			}
		case strings.HasPrefix(trimmed, "Duration: ") && cur == nil:
			d, _, _ := strings.Cut(strings.TrimPrefix(trimmed, "Duration: "), ",")
			mf.Duration = clockSeconds(d)
		case strings.HasPrefix(trimmed, "Stream #0:"):
			cur = nil
			n, t, ok := dumpStream(strings.TrimPrefix(trimmed, "Stream #0:"))
			if ok && !seen[n] {
				seen[n] = true
				cur = &stream{n: n, t: t, indent: indent}
				streams = append(streams, cur)
			}
		case cur != nil && indent <= cur.indent:
			cur = nil // a program, chapters: past this stream's metadata
		case cur != nil:
			if k, v, ok := strings.Cut(trimmed, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "title") {
				cur.t.Title = strings.TrimSpace(v)
			}
		}
	}
	if mf.Container == "" {
		mf.Duration = 0
		return
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].n < streams[j].n })
	for _, s := range streams {
		if s.t.Kind != "" {
			mf.Tracks = append(mf.Tracks, s.t)
		}
	}
}

// dumpStream reads a stream's line after "Stream #0:": its index and track,
// with no Kind for one that isn't a track (attachments, data, cover art).
//
//	1[0x2](eng): Audio: aac (LC) (mp4a / 0x6134706D), 48000 Hz, stereo, fltp, 125 kb/s (default)
func dumpStream(s string) (int, Track, bool) {
	var t Track
	head, rest, ok := strings.Cut(s, ": ")
	if !ok {
		return 0, t, false
	}
	num := head
	if i := strings.IndexAny(num, "[("); i >= 0 {
		num = num[:i]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, t, false
	}
	if i := strings.Index(head, "("); i >= 0 && strings.HasSuffix(head, ")") {
		if l := strings.ToLower(head[i+1 : len(head)-1]); l != "und" {
			t.Lang = l
		}
	}
	kind, desc, _ := strings.Cut(rest, ": ")
	switch kind {
	case "Video", "Audio", "Subtitle":
	default:
		return n, Track{}, true
	}
	if strings.Contains(desc, "(attached pic)") {
		return n, Track{}, true
	}
	t.Kind = strings.ToLower(kind)
	t.Codec = desc
	if i := strings.IndexAny(desc, " ,"); i >= 0 {
		t.Codec = desc[:i]
	}
	t.Default = strings.Contains(desc, "(default)")
	t.Forced = strings.Contains(desc, "(forced)")
	t.HearingImpaired = strings.Contains(desc, "(hearing impaired)")
	t.Commentary = strings.Contains(desc, "(comment)")
	fields := strings.Split(desc, ", ")
	for i, f := range fields {
		switch {
		case t.Kind == "audio" && strings.HasSuffix(f, " Hz") && i+1 < len(fields):
			t.Channels = channelCount(fields[i+1])
		case t.Kind == "video" && t.Width == 0:
			size, _, _ := strings.Cut(f, " ")
			w, h, ok := strings.Cut(size, "x")
			if wn, err := strconv.Atoi(w); ok && err == nil && wn > 0 && i > 0 {
				if hn, err := strconv.Atoi(h); err == nil && hn > 0 {
					t.Width, t.Height = wn, hn
				}
			}
		}
	}
	return n, t, true
}

// channelCount turns a channel layout's name into a number of channels:
// "stereo" 2, "5.1(side)" 6, "6 channels" 6. 0 if it's not one it knows.
func channelCount(layout string) int {
	layout, _, _ = strings.Cut(layout, "(")
	switch layout {
	case "mono":
		return 1
	case "stereo", "downmix":
		return 2
	case "quad":
		return 4
	case "hexagonal":
		return 6
	case "octagonal":
		return 8
	}
	if n, ok := strings.CutSuffix(layout, " channels"); ok {
		return Atoi(n)
	}
	if a, b, ok := strings.Cut(layout, "."); ok {
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		if err1 == nil && err2 == nil {
			return x + y
		}
	}
	return 0
}

// clockSeconds reads "01:02:03.45" as seconds; 0 for anything else ("N/A").
func clockSeconds(s string) float64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return float64(h*3600+m*60) + sec
}
