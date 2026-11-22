package library

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mvd/internal/testutil"
)

// The Star City case, as ffprobe 5.1 on the router reported it (cut to four
// of its 43 subtitle tracks): no languages or titles, one forced track, an
// attached font, and no software HEVC decoder (which only affects stderr
// and the exit status).
const starCityJSON = `{"programs":[],"streams":[
{"codec_name":"hevc","codec_type":"video","width":1920,"height":1080,"disposition":{"default":1,"comment":0,"forced":0,"hearing_impaired":0},"tags":{"DURATION":"00:52:01.000000000"}},
{"codec_name":"eac3","codec_type":"audio","channels":6,"disposition":{"default":1,"comment":0,"forced":0,"hearing_impaired":0},"tags":{"language":"und"}},
{"codec_name":"subrip","codec_type":"subtitle","disposition":{"default":1,"comment":0,"forced":1,"hearing_impaired":0},"tags":{}},
{"codec_name":"subrip","codec_type":"subtitle","disposition":{"default":0,"comment":0,"forced":0,"hearing_impaired":0}},
{"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"font.ttf"}},
{"codec_name":"mjpeg","codec_type":"video","width":600,"height":900,"disposition":{"attached_pic":1}},
{"codec_name":"ass","codec_type":"subtitle","disposition":{"default":0,"comment":0,"forced":0,"hearing_impaired":1},"tags":{"LANGUAGE":"ENG","Title":" Signs "}}
],"format":{"format_name":"matroska,webm","duration":"3121.000000"}}`

func TestParseProbe(t *testing.T) {
	mf := &MediaFile{}
	parseProbe(mf, []byte(starCityJSON), "/tv/Star City/S01E01.mkv")
	want := &MediaFile{Container: "mkv", Duration: 3121, Tracks: []Track{
		{Kind: "video", Codec: "hevc", Default: true, Width: 1920, Height: 1080},
		{Kind: "audio", Codec: "eac3", Default: true, Channels: 6},
		{Kind: "subtitle", Codec: "subrip", Default: true, Forced: true},
		{Kind: "subtitle", Codec: "subrip"},
		{Kind: "subtitle", Codec: "ass", Lang: "eng", Title: "Signs", HearingImpaired: true},
	}}
	if !reflect.DeepEqual(mf, want) {
		t.Errorf("got  %+v\nwant %+v", mf, want)
	}

	// Nothing usable: no Container, so it's known to have failed.
	for _, out := range []string{"", "{}", "not json", `{"streams":[]}`} {
		mf := &MediaFile{}
		if parseProbe(mf, []byte(out), "x.mkv"); mf.OK() || mf.Tracks != nil {
			t.Errorf("%q: %+v", out, mf)
		}
	}
}

func TestContainerName(t *testing.T) {
	for _, c := range []struct{ format, path, want string }{
		{"matroska,webm", "a.mkv", "mkv"},
		{"matroska,webm", "a.webm", "webm"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.mp4", "mp4"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.M4V", "m4v"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.mov", "mov"},
		{"avi", "a.avi", "avi"},
		{"mpegts", "a.m2ts", "ts"},
		{"asf", "a.wmv", "wmv"},
	} {
		if got := containerName(c.format, c.path); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.format, c.path, got, c.want)
		}
	}
}

func needProbe(t *testing.T, path string) {
	t.Helper()
	if FFprobe() == "" {
		t.Skip("needs ffprobe")
	}
	if !testutil.WriteClip(t, path) {
		t.Skip("needs ffmpeg")
	}
}

func TestProbe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clip.mkv")
	needProbe(t, p)
	mf, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	want := []Track{
		{Kind: "video", Codec: "h264", Width: 64, Height: 48},
		{Kind: "audio", Codec: "aac", Lang: "eng", Default: true, Channels: 2},
		{Kind: "audio", Codec: "aac", Lang: "eng", Title: "Director", Commentary: true, Channels: 1},
		{Kind: "subtitle", Codec: "subrip", Default: true, Forced: true},
		{Kind: "subtitle", Codec: "subrip", Lang: "fre", HearingImpaired: true},
		{Kind: "subtitle", Codec: "subrip"},
	}
	// ffmpeg marks the first video track default whatever it's told.
	if len(mf.Tracks) > 0 {
		mf.Tracks[0].Default = false
	}
	if mf.Container != "mkv" || mf.Duration < 0.9 || mf.Duration > 1.5 || mf.Size != st.Size() ||
		!mf.ModTime.Equal(st.ModTime()) || !reflect.DeepEqual(mf.Tracks, want) {
		t.Errorf("got %+v\ntracks %+v", mf, mf.Tracks)
	}
	if m := probeMinutes(p); m != 0 {
		t.Errorf("a second rounds to 0 minutes, got %d", m)
	}

	// Not a video: probed, but not OK.
	junk := filepath.Join(t.TempDir(), "junk.mkv")
	testutil.Write(t, junk, "\x1a\x45\xdf\xa3 not really")
	if mf, err := Probe(junk); err != nil || mf.OK() || mf.Size != 15 {
		t.Errorf("junk: %+v %v", mf, err)
	}
	if _, err := Probe(filepath.Join(t.TempDir(), "gone.mkv")); err == nil {
		t.Error("a missing file should be an error")
	}
}

// A rescan keeps a file's tracks while its size and time are the same.
func TestBuildIndexKeepsProbes(t *testing.T) {
	root := t.TempDir()
	movies, tv := filepath.Join(root, "movies"), filepath.Join(root, "tv")
	a := filepath.Join(movies, "Alpha (2001)/Alpha (2001).mkv")
	b1 := filepath.Join(movies, "Beta (2002)/Beta (2002) part1.mkv")
	b2 := filepath.Join(movies, "Beta (2002)/Beta (2002) part2.mkv")
	e1 := filepath.Join(tv, "Show/Season 1/Show.S01E01.mkv")
	e2 := filepath.Join(tv, "Show/Season 1/Show.S01E02.mkv")
	for _, p := range []string{a, b1, b2, e1, e2} {
		testutil.Write(t, p, "\x1a\x45\xdf\xa3 matroska")
	}

	ix := BuildIndex(movies, tv, nil)
	if got := ix.Unprobed(); !reflect.DeepEqual(got, []string{a, b1, b2, e1, e2}) {
		t.Fatalf("unprobed: %q", got)
	}
	probed := Probed{}
	for _, p := range []string{a, b2, e1, e2} {
		st, _ := os.Stat(p)
		probed[p] = &MediaFile{V: probeVersion, Size: st.Size(), ModTime: st.ModTime(), Container: "mkv"}
	}
	nx := ix.WithMedia(probed)
	if got := nx.Unprobed(); !reflect.DeepEqual(got, []string{b1}) {
		t.Errorf("unprobed after: %q", got)
	}
	if nx.Bytes() != ix.Bytes() || ix.Bytes() == 0 {
		t.Errorf("size: %d in the copy, %d before", nx.Bytes(), ix.Bytes())
	}
	// The old index is left as it was, for whoever's reading it.
	if len(ix.Unprobed()) != 5 || ix.Movies[0].Media != nil {
		t.Error("WithMedia changed the index it copied")
	}
	ep := nx.Episode(nx.Shows[0].Seasons[0].Episodes[0].ID)
	if ep.Media == nil || ep.Show != nx.Shows[0] || nx.Shows[0] == ix.Shows[0] {
		t.Errorf("episode in the copy: %+v", ep)
	}
	if beta := nx.Movies[1]; len(beta.Media) != 2 || beta.Media[0] != nil || beta.Media[1] == nil {
		t.Errorf("parts: %+v", beta.Media)
	}

	// Replace one file, and touch another: both are probed again. So is one
	// probed by an older mvd (before failures could be read).
	testutil.Write(t, a, "\x1a\x45\xdf\xa3 a longer matroska")
	later := time.Now().Add(time.Hour)
	os.Chtimes(e2, later, later)
	known := nx.Probed()
	old := *known[e1]
	old.V = 0
	known[e1] = &old
	rx := BuildIndex(movies, tv, known)
	if got := rx.Unprobed(); !reflect.DeepEqual(got, []string{a, b1, e1, e2}) {
		t.Errorf("unprobed after a rescan: %q", got)
	}

	// And it's saved with the index.
	path := filepath.Join(t.TempDir(), "index.json")
	if err := nx.Save(path); err != nil {
		t.Fatal(err)
	}
	lx, err := LoadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := lx.Unprobed(); !reflect.DeepEqual(got, []string{b1}) {
		t.Errorf("unprobed after loading: %q", got)
	}
}

// What ffprobe 5.1 on the router prints for Star City S01E01 (cut to five of
// its 43 subtitle tracks), with no JSON on stdout: it stops when it can't
// open the HEVC decoder, after printing this.
const starCityDump = `[hevc_v4l2m2m @ 0x42b580] Could not find a valid device
[hevc_v4l2m2m @ 0x42b580] can't configure decoder
[matroska,webm @ 0x76683cb0] Failed to open codec in avformat_find_stream_info
Input #0, matroska,webm, from '/mnt/Maxtor/video/tv/Star City (2026)/Season 01/Star City (2026) - S01E01 - The Eyes.mkv':
  Metadata:
    ENCODER         : Lavf62.15.100
  Duration: 00:59:52.45, start: 0.000000, bitrate: 2878 kb/s
  Stream #0:0: Video: hevc, none(tv, bt709, progressive), 2376x1080, SAR 1:1 DAR 11:5, 23.98 fps, 23.98 tbr, 1k tbn (default)
    Metadata:
      ENCODER         : Lavc62.30.100 libx265
      DURATION        : 00:59:52.422000000
  Stream #0:1: Audio: eac3, 48000 Hz, 5.1(side), fltp, 768 kb/s (default)
    Metadata:
      DURATION        : 00:59:52.448000000
  Stream #0:2: Subtitle: subrip (default) (forced)
    Metadata:
      DURATION        : 00:39:55.687000000
  Stream #0:3: Subtitle: subrip
    Metadata:
      DURATION        : 00:57:35.497000000
  Stream #0:4: Subtitle: subrip
    Metadata:
      DURATION        : 00:57:51.346000000
  Stream #0:5(eng): Subtitle: ass (hearing impaired)
    Metadata:
      title           : Signs
      DURATION        : 00:59:45.335000000
  Stream #0:6: Attachment: ttf
    Metadata:
      filename        : font.ttf
[hevc_v4l2m2m @ 0x42b580] Could not find a valid device
[hevc_v4l2m2m @ 0x42b580] can't configure decoder
`

// An MP4 as ffprobe 6.1 prints it: stream ids in brackets, codec tags, cover
// art, a chapter with a title of its own.
const mp4Dump = `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'a.m4v':
  Metadata:
    major_brand     : isom
    title           : The Film
  Duration: 01:42:07.15, start: 0.000000, bitrate: 2100 kb/s
  Chapters:
    Chapter #0:0: start 0.000000, end 600.000000
      Metadata:
        title           : Opening
  Stream #0:0[0x1](und): Video: h264 (High) (avc1 / 0x31637661), yuv420p(progressive), 1920x800 [SAR 1:1 DAR 12:5], 1900 kb/s, 23.98 fps, 23.98 tbr, 24k tbn (default)
    Metadata:
      handler_name    : VideoHandler
  Stream #0:1[0x2](spa): Audio: aac (LC) (mp4a / 0x6134706D), 48000 Hz, stereo, fltp, 125 kb/s (default)
    Metadata:
      handler_name    : SoundHandler
  Stream #0:2[0x3](eng): Audio: ac3 (ac-3 / 0x332D6361), 48000 Hz, 7.1, fltp, 640 kb/s (comment)
    Metadata:
      title           : Director
  Stream #0:3[0x4](eng): Subtitle: mov_text (tx3g / 0x67337874), 0 kb/s (forced)
  Stream #0:4[0x0]: Video: mjpeg (Baseline), yuvj420p(pc, bt470bg/unknown/unknown), 600x900, 90k tbr, 90k tbn (attached pic)
`

func TestParseDump(t *testing.T) {
	mf := &MediaFile{}
	parseDump(mf, []byte(starCityDump), "/tv/Star City/S01E01.mkv")
	want := &MediaFile{Container: "mkv", Duration: 3592.45, Tracks: []Track{
		{Kind: "video", Codec: "hevc", Default: true, Width: 2376, Height: 1080},
		{Kind: "audio", Codec: "eac3", Default: true, Channels: 6},
		{Kind: "subtitle", Codec: "subrip", Default: true, Forced: true},
		{Kind: "subtitle", Codec: "subrip"},
		{Kind: "subtitle", Codec: "subrip"},
		{Kind: "subtitle", Codec: "ass", Lang: "eng", Title: "Signs", HearingImpaired: true},
	}}
	if !reflect.DeepEqual(mf, want) {
		t.Errorf("Star City:\ngot  %+v\nwant %+v", mf, want)
	}

	mf = &MediaFile{}
	parseDump(mf, []byte(mp4Dump), "/movies/a.m4v")
	want = &MediaFile{Container: "m4v", Duration: 6127.15, Tracks: []Track{
		{Kind: "video", Codec: "h264", Default: true, Width: 1920, Height: 800},
		{Kind: "audio", Codec: "aac", Lang: "spa", Default: true, Channels: 2},
		{Kind: "audio", Codec: "ac3", Lang: "eng", Title: "Director", Commentary: true, Channels: 8},
		{Kind: "subtitle", Codec: "mov_text", Lang: "eng", Forced: true},
	}}
	if !reflect.DeepEqual(mf, want) {
		t.Errorf("MP4:\ngot  %+v\nwant %+v", mf, want)
	}

	for _, out := range []string{"", "a.mkv: Invalid data found when processing input\n"} {
		mf := &MediaFile{}
		if parseDump(mf, []byte(out), "a.mkv"); mf.OK() {
			t.Errorf("%q: %+v", out, mf)
		}
	}
}

// The description and the JSON say the same about a file.
func TestParseDumpMatchesJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clip.mkv")
	needProbe(t, p)
	stdout, stderr := runFFprobe(FFprobe(), p)
	fromJSON, fromDump := &MediaFile{}, &MediaFile{}
	if !parseProbe(fromJSON, stdout, p) {
		t.Fatalf("no JSON: %s", stdout)
	}
	parseDump(fromDump, stderr, p)
	fromDump.Duration = fromJSON.Duration // the description rounds it to centiseconds
	if !reflect.DeepEqual(fromJSON, fromDump) {
		t.Errorf("JSON  %+v\ndump  %+v", fromJSON, fromDump)
	}
}

func TestChannelCount(t *testing.T) {
	for layout, want := range map[string]int{"mono": 1, "stereo": 2, "5.1(side)": 6, "7.1": 8,
		"2.1": 3, "quad": 4, "6 channels": 6, "downmix": 2, "": 0, "hexagonal": 6} {
		if got := channelCount(layout); got != want {
			t.Errorf("%q: %d, want %d", layout, got, want)
		}
	}
}

// The router's ffprobe, end to end: "{" on stdout, the description on
// stderr, and exit 1.
func TestProbeStopsShort(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, "dump.txt"), starCityDump)
	fake := filepath.Join(dir, "ffprobe")
	testutil.Write(t, fake, "#!/bin/sh\necho '{'\ncat '"+filepath.Join(dir, "dump.txt")+"' >&2\nexit 1\n")
	os.Chmod(fake, 0o755)

	stdout, stderr := runFFprobe(fake, "/tv/Star City/S01E01.mkv")
	mf := &MediaFile{}
	readProbe(mf, stdout, stderr, "/tv/Star City/S01E01.mkv")
	if mf.Container != "mkv" || len(mf.Tracks) != 6 || !mf.Tracks[2].Forced {
		t.Errorf("%+v", mf)
	}
}
