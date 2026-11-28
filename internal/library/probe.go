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
// container, the duration and bitrate, each track's codec, language, title
// and flags, and what the picture and sound are (profile, bit depth, shape,
// frame rate, HDR). Only the file's own headers and tags, no guessing.
// Without ffprobe nothing is probed and clients do without.

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
	Bitrate   int       `json:"bitrate,omitempty"`  // kb/s, the whole file's
	Tracks    []Track   `json:"tracks,omitempty"`
}

// OK says ffprobe read the file.
func (mf *MediaFile) OK() bool { return mf != nil && mf.Container != "" }

// Track is one video, audio or subtitle stream, in the file's order.
type Track struct {
	Kind            string `json:"kind"` // video, audio or subtitle
	Codec           string `json:"codec,omitempty"`
	Profile         string `json:"profile,omitempty"` // video and audio: "High 10", "DTS-HD MA"
	Lang            string `json:"lang,omitempty"`
	Title           string `json:"title,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearing_impaired,omitempty"`
	Commentary      bool   `json:"commentary,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	Bitrate         int    `json:"bitrate,omitempty"` // kb/s, video and audio, where the container says (not MKV)

	// The picture, for video tracks.
	Tag        string `json:"tag,omitempty"`        // the codec tag, where the container has one: avc1, hev1, XVID
	Bits       int    `json:"bits,omitempty"`       // 8, 10, 12, from the pixel format
	SAR        string `json:"sar,omitempty"`        // pixel shape, when it isn't 1:1: "16:15"
	DAR        string `json:"dar,omitempty"`        // the picture's shape, as ffprobe works it out: "16:9"
	FPS        string `json:"fps,omitempty"`        // as a fraction: "24000/1001"
	Interlaced bool   `json:"interlaced,omitempty"` // flagged interlaced, which isn't always so
	HDR        string `json:"hdr,omitempty"`        // pq (HDR10, HDR10+) or hlg
	DV         int    `json:"dv,omitempty"`         // the Dolby Vision profile, when there's a configuration record
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
// again. 2: reading ffprobe's description when its JSON stops short. 3:
// the picture and sound (profile, bit depth, shape, frame rate, HDR).
const probeVersion = 3

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
		"-show_entries", "format=format_name,duration,bit_rate"+
			":stream=index,codec_type,codec_name,profile,codec_tag_string,channels,width,height"+
			",pix_fmt,sample_aspect_ratio,display_aspect_ratio,avg_frame_rate,field_order,color_transfer,bit_rate"+
			":stream_tags=language,title"+
			":stream_disposition=default,forced,hearing_impaired,comment,attached_pic"+
			":stream_side_data=side_data_type,dv_profile",
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
		Profile     string            `json:"profile"`
		CodecTag    string            `json:"codec_tag_string"`
		Channels    int               `json:"channels"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		PixFmt      string            `json:"pix_fmt"`
		SAR         string            `json:"sample_aspect_ratio"`
		DAR         string            `json:"display_aspect_ratio"`
		FPS         string            `json:"avg_frame_rate"`
		FieldOrder  string            `json:"field_order"`
		Transfer    string            `json:"color_transfer"`
		Bitrate     string            `json:"bit_rate"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
		SideData    []struct {
			Type      string `json:"side_data_type"`
			DVProfile int    `json:"dv_profile"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Bitrate    string `json:"bit_rate"`
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
	mf.Bitrate = kbps(p.Format.Bitrate)
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
			t.Bitrate = kbps(s.Bitrate)
			t.Channels = s.Channels
			t.Profile = known(s.Profile)
		case "video":
			t.Width, t.Height = s.Width, s.Height
			t.Bitrate = kbps(s.Bitrate)
			t.Profile = known(s.Profile)
			if tg := s.CodecTag; !strings.HasPrefix(tg, "[") {
				t.Tag = tg // "[0][0][0][0]" is none
			}
			t.Bits = pixBits(s.PixFmt)
			t.SAR, t.DAR = shape(s.SAR, s.DAR)
			t.FPS = fpsFraction(s.FPS)
			t.Interlaced = s.FieldOrder == "tt" || s.FieldOrder == "bb" || s.FieldOrder == "tb" || s.FieldOrder == "bt"
			t.HDR = hdrKind(s.Transfer)
			for _, sd := range s.SideData {
				if sd.Type == "DOVI configuration record" {
					t.DV = sd.DVProfile
				}
			}
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

// known is ffprobe's value, or "" for one it doesn't know.
func known(s string) string {
	if s == "unknown" || s == "N/A" {
		return ""
	}
	return s
}

// kbps reads ffprobe's bits a second as kb/s, as its description has them.
func kbps(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n / 1000
}

// pixBits is a pixel format's bit depth: yuv420p 8, yuv420p10le 10, p010le
// 10. 0 for a format it doesn't know ("none" when ffprobe has no decoder).
func pixBits(f string) int {
	if strings.HasPrefix(f, "p0") && len(f) >= 4 { // p010le, p016le
		return Atoi(f[1:4])
	}
	if !strings.HasPrefix(f, "yuv") && !strings.HasPrefix(f, "nv") && !strings.HasPrefix(f, "gray") {
		return 0
	}
	f = strings.TrimSuffix(strings.TrimSuffix(f, "le"), "be")
	i := len(f)
	for i > 0 && f[i-1] >= '0' && f[i-1] <= '9' {
		i--
	}
	if i < len(f) && i > 0 && f[i-1] == 'p' {
		if n := Atoi(f[i:]); n > 8 && n <= 16 {
			return n
		}
	}
	return 8
}

// fpsFraction is ffprobe's frame rate ("24000/1001", or "23.98" from its
// description, which rounds it) as a fraction, snapped to the rate it's
// near when there is one: an MP4's average can come out as
// "11520000/480479" for a film at 24000/1001. "" when it's unknown.
func fpsFraction(s string) string {
	var f float64
	exact := ""
	if n, d, ok := strings.Cut(s, "/"); ok {
		nn, dn := Atoi(n), Atoi(d)
		if nn == 0 || dn == 0 {
			return ""
		}
		f, exact = float64(nn)/float64(dn), s
	} else {
		if k, ok := strings.CutSuffix(s, "k"); ok {
			s = k + "000"
		}
		var err error
		if f, err = strconv.ParseFloat(s, 64); err != nil || f <= 0 {
			return ""
		}
	}
	for _, n := range []int{12, 15, 24, 30, 48, 60, 120} { // NTSC's
		if r := float64(n*1000) / 1001; f > r-0.006 && f < r+0.006 {
			return strconv.Itoa(n*1000) + "/1001"
		}
	}
	if r := int(f + 0.5); r > 0 && f > float64(r)-0.006 && f < float64(r)+0.006 {
		return strconv.Itoa(r) + "/1"
	}
	if exact != "" {
		return exact
	}
	n, d := int(f*100+0.5), 100 // the description has two decimals
	for a, b := n, d; ; {
		if b == 0 {
			return strconv.Itoa(n/a) + "/" + strconv.Itoa(d/a)
		}
		a, b = b, a%b
	}
}

// shape is the pixel shape, left out when it's square, and the picture's,
// leaving out ratios ffprobe doesn't know ("0:1", "N/A").
func shape(sar, dar string) (string, string) {
	ok := func(r string) bool {
		n, d, found := strings.Cut(r, ":")
		return found && Atoi(n) > 0 && Atoi(d) > 0
	}
	if !ok(sar) || sar == "1:1" {
		sar = ""
	}
	if !ok(dar) {
		dar = ""
	}
	return sar, dar
}

// hdrKind is the HDR kind from the transfer function: pq for HDR10 and
// HDR10+, hlg, or "" for SDR.
func hdrKind(transfer string) string {
	switch transfer {
	case "smpte2084":
		return "pq"
	case "arib-std-b67":
		return "hlg"
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
			if _, b, ok := strings.Cut(trimmed, "bitrate: "); ok {
				mf.Bitrate = Atoi(strings.TrimSuffix(b, " kb/s"))
			}
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
			k, v, ok := strings.Cut(trimmed, ":")
			switch {
			case !ok:
			case strings.EqualFold(strings.TrimSpace(k), "title"):
				cur.t.Title = strings.TrimSpace(v)
			case k == "DOVI configuration record" && cur.t.Kind == "video":
				// version: 1.0, profile: 8, level: 6, rpu flag: 1, ...
				for _, f := range strings.Split(v, ", ") {
					if p, ok := strings.CutPrefix(strings.TrimSpace(f), "profile: "); ok {
						cur.t.DV = Atoi(p)
					}
				}
			}
		}
	}
	if mf.Container == "" {
		mf.Duration, mf.Bitrate = 0, 0
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
	t.Default = strings.Contains(desc, "(default)")
	t.Forced = strings.Contains(desc, "(forced)")
	t.HearingImpaired = strings.Contains(desc, "(hearing impaired)")
	t.Commentary = strings.Contains(desc, "(comment)")
	fields := splitTop(desc)
	// "hevc (Main 10) (hev1 / 0x31766568)": the codec, its profile, its tag.
	t.Codec, _, _ = strings.Cut(fields[0], " ")
	if t.Kind != "subtitle" { // a subtitle's are its flags: "subrip (default)"
		for _, g := range parens(fields[0]) {
			if tg, _, ok := strings.Cut(g, " / 0x"); ok {
				if t.Kind == "video" && !strings.HasPrefix(tg, "[") {
					t.Tag = tg
				}
			} else if t.Profile == "" {
				t.Profile = g
			}
		}
	}
	for i, f := range fields {
		if i == 0 {
			continue
		}
		f, _, _ = strings.Cut(f, " (") // the flags, after the last
		switch {
		case strings.HasSuffix(f, " kb/s") && t.Kind != "subtitle":
			t.Bitrate = Atoi(strings.TrimSuffix(f, " kb/s"))
		case t.Kind == "audio" && strings.HasSuffix(f, " Hz") && i+1 < len(fields):
			t.Channels = channelCount(fields[i+1])
		case t.Kind != "video":
		case i == 1 && (f == "" || f[0] < '0' || f[0] > '9'):
			dumpPixels(&t, fields[i])
		case strings.HasPrefix(f, "SAR "):
			dumpShape(&t, f)
		case strings.HasSuffix(f, " fps"):
			t.FPS = fpsFraction(strings.TrimSuffix(f, " fps"))
		case t.Width == 0:
			size, rest, _ := strings.Cut(f, " ")
			w, h, ok := strings.Cut(size, "x")
			if wn, err := strconv.Atoi(w); ok && err == nil && wn > 0 {
				if hn, err := strconv.Atoi(h); err == nil && hn > 0 {
					t.Width, t.Height = wn, hn
					dumpShape(&t, strings.Trim(rest, "[]"))
				}
			}
		}
	}
	if t.Kind == "video" && t.Profile == "unknown" {
		t.Profile = ""
	}
	return n, t, true
}

// splitTop splits a stream's description at its commas, leaving those
// inside parentheses: "yuv420p10le(tv, bt709, progressive)" is one field.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 && i+1 < len(s) && s[i+1] == ' ' {
				out = append(out, s[start:i])
				start = i + 2
			}
		}
	}
	return append(out, s[start:])
}

// parens lists what's in a field's top-level parentheses, in order:
// "h264 (High) (avc1 / 0x31637661)" gives "High" and "avc1 / 0x31637661".
func parens(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			if depth--; depth == 0 {
				out = append(out, s[start:i])
			}
		}
	}
	return out
}

// dumpPixels reads the pixel format field of a video stream's description:
// "yuv420p10le(tv, bt2020nc/bt2020/smpte2084, top first)". The colors are
// one name when the space, primaries and transfer agree ("bt709"), else
// all three; the field order is "progressive", "top first", "bottom
// first", or either "coded first (swapped)".
func dumpPixels(t *Track, f string) {
	name, rest, _ := strings.Cut(f, "(")
	t.Bits = pixBits(strings.TrimSpace(name))
	for _, p := range strings.Split(strings.TrimSuffix(rest, ")"), ", ") {
		switch {
		case strings.HasPrefix(p, "top ") || strings.HasPrefix(p, "bottom "):
			t.Interlaced = true
		case strings.Count(p, "/") == 2:
			t.HDR = hdrKind(p[strings.LastIndex(p, "/")+1:])
		default:
			if t.HDR == "" {
				t.HDR = hdrKind(p)
			}
		}
	}
}

// dumpShape reads "SAR 1:1 DAR 16:9". The stream's, after the size, comes
// after the codec's, in the size's brackets, and wins.
func dumpShape(t *Track, f string) {
	parts := strings.Fields(f)
	if len(parts) == 4 && parts[0] == "SAR" && parts[2] == "DAR" {
		t.SAR, t.DAR = shape(parts[1], parts[3])
	}
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
