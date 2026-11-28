package remux

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// elem is an element for the hand-made files: its ID, an 8-byte size (so
// offsets are easy to work out) and its body.
func elem(id uint64, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	var out []byte
	for v := id; v > 0; v >>= 8 {
		out = append([]byte{byte(v)}, out...)
	}
	size := []byte{0x01, 0, 0, 0, 0, 0, 0, 0}
	n := len(b)
	for i := 7; i > 0; i-- {
		size[i] = byte(n)
		n >>= 8
	}
	return append(append(out, size...), b...)
}

func uintEl(id uint64, v uint64) []byte {
	return elem(id, []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func floatEl(id uint64, f float64) []byte {
	var b [8]byte
	bits := math.Float64bits(f)
	for i := 0; i < 8; i++ {
		b[i] = byte(bits >> (56 - 8*i))
	}
	return elem(id, b[:])
}

// mkv makes a file: header, segment, then the segment's children in the
// order given. withSeekHead puts a SeekHead first, pointing at each
// part's position, worked out from the parts' sizes.
func mkv(withSeekHead bool, parts ...[]byte) []byte {
	ids := []uint64{}
	for _, p := range parts {
		id, _, _, _ := readHeader(bytes.NewReader(p))
		ids = append(ids, id)
	}
	var body []byte
	if withSeekHead {
		// Each Seek is the same size, so the SeekHead's is known first.
		seek := func(id, pos uint64) []byte {
			return elem(idSeek, uintEl(idSeekID, id), elem(idSeekPosition, []byte{
				byte(pos >> 56), byte(pos >> 48), byte(pos >> 40), byte(pos >> 32),
				byte(pos >> 24), byte(pos >> 16), byte(pos >> 8), byte(pos)}))
		}
		headSize := len(elem(idSeekHead, bytes.Repeat(seek(0, 0), len(parts))))
		var seeks [][]byte
		pos := uint64(headSize)
		for i, p := range parts {
			seeks = append(seeks, seek(ids[i], pos))
			pos += uint64(len(p))
		}
		body = elem(idSeekHead, seeks...)
	}
	for _, p := range parts {
		body = append(body, p...)
	}
	return append(elem(idEBML, uintEl(0x4286, 1)), elem(idSegment, body)...)
}

func info(ms float64) []byte {
	return elem(idInfo, uintEl(idTimestampScale, 1000000), floatEl(idDuration, ms))
}

func tracks() []byte {
	return elem(idTracks,
		elem(idTrackEntry, uintEl(idTrackNumber, 1), uintEl(idTrackType, 2)), // audio first
		elem(idTrackEntry, uintEl(idTrackNumber, 2), uintEl(idTrackType, 1)))
}

func cues(points ...[2]uint64) []byte {
	var ps [][]byte
	for _, p := range points {
		ps = append(ps, elem(idCuePoint, uintEl(idCueTime, p[0]),
			elem(idCueTrackPos, uintEl(idCueTrack, p[1]), uintEl(0xF1, 0))))
	}
	return elem(idCues, ps...)
}

func cluster() []byte { return elem(idCluster, uintEl(0xE7, 0), elem(0xA3, make([]byte, 1000))) }

func TestReadIndexMadeByHand(t *testing.T) {
	want := &Index{Keyframes: []float64{0, 2.5, 4.25}, Duration: 6}
	c := cues([2]uint64{0, 2}, [2]uint64{0, 1}, [2]uint64{2500, 2}, [2]uint64{3000, 1}, [2]uint64{4250, 2})
	for name, f := range map[string][]byte{
		"Cues at the end, found by the SeekHead": mkv(true, info(6000), tracks(), cluster(), cluster(), c),
		"no SeekHead: walked to":                 mkv(false, info(6000), tracks(), cluster(), cluster(), c),
		"Cues before the clusters":               mkv(false, info(6000), tracks(), c, cluster()),
	} {
		got, err := readIndex(bytes.NewReader(f))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	for name, f := range map[string][]byte{
		"no Cues":                  mkv(true, info(6000), tracks(), cluster()),
		"no video's cues":          mkv(true, info(6000), tracks(), cluster(), cues([2]uint64{0, 1})),
		"no video track":           mkv(true, info(6000), elem(idTracks), cluster(), c),
		"no length":                mkv(true, info(0), tracks(), cluster(), c),
		"not Matroska":             []byte("RIFF....AVI LIST"),
		"empty":                    nil,
		"cut off":                  mkv(true, info(6000), tracks(), cluster(), c)[:200],
		"a size past what's there": append(mkv(false, info(6000)), 0x1C, 0x53, 0xBB, 0x6B, 0x88, 0, 0, 0, 0, 0, 1),
	} {
		if got, err := readIndex(bytes.NewReader(f)); err == nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if _, err := readIndex(bytes.NewReader(mkv(true, info(6000), tracks(), cluster()))); !errors.Is(err, ErrNoIndex) {
		t.Errorf("no Cues: %v", err)
	}
}

func TestPlaylist(t *testing.T) {
	ix := &Index{Keyframes: []float64{0, 2.002, 4.5}, Duration: 10.25}
	want := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="/remux/m1/init.mp4?part=1&audio=0"
#EXTINF:2.002,
/remux/m1/00000.m4s?part=1&audio=0
#EXTINF:2.498,
/remux/m1/00001.m4s?part=1&audio=0
#EXTINF:5.750,
/remux/m1/00002.m4s?part=1&audio=0
#EXT-X-ENDLIST
`
	if got := string(ix.Playlist("/remux/m1/", "?part=1&audio=0", 0)); got != want {
		t.Errorf("got\n%s", got)
	}
	if got := string(ix.Playlist("/", "", 4.5)); !strings.Contains(got, "#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-START:TIME-OFFSET=4.500,PRECISE=YES\n") {
		t.Errorf("starting partway:\n%s", got)
	}
	for at, seg := range map[float64]int{0: 0, 1: 0, 2.002: 1, 4.4: 1, 4.5: 2, 9: 2, 100: 2, -1: 0} {
		if got := ix.Segment(at); got != seg {
			t.Errorf("at %v: segment %d, want %d", at, got, seg)
		}
	}
	if s := ix.seekTo(1); s <= 2.002 || s >= 4.5 {
		t.Errorf("seek for segment 1: %v", s)
	}
}

// clip makes a real MKV of secs seconds with a keyframe every second, and
// AAC audio, when ffmpeg is installed.
func clip(t *testing.T, secs int) string {
	t.Helper()
	if FFmpeg() == "" {
		t.Skip("needs ffmpeg")
	}
	p := filepath.Join(t.TempDir(), "clip.mkv")
	d := strconv.Itoa(secs)
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=160x120:r=24000/1001:d="+d,
		"-f", "lavfi", "-i", "sine=f=440:r=48000:d="+d,
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "24", "-bf", "2",
		"-c:a", "aac", "-f", "matroska", p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return p
}

func TestReadIndexFromFFmpeg(t *testing.T) {
	p := clip(t, 10)
	ix, err := ReadIndex(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Keyframes) != 10 || ix.Keyframes[0] != 0 || ix.Keyframes[1] != 1.001 || ix.Duration < 10 || ix.Duration > 10.2 {
		t.Errorf("%+v", ix)
	}
}

func newManager(t *testing.T) *Manager {
	m := New(FFmpeg(), filepath.Join(t.TempDir(), "remux"))
	m.Wait = 20 * time.Second
	t.Cleanup(m.Stop)
	return m
}

// packets lists what's in init.mp4 and a segment, as ffprobe reads them.
func packets(t *testing.T, init, seg string) string {
	t.Helper()
	both := filepath.Join(t.TempDir(), "both.mp4")
	a, _ := os.ReadFile(init)
	b, _ := os.ReadFile(seg)
	os.WriteFile(both, append(a, b...), 0o644)
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "packet=stream_index,pts_time,dts_time,size,flags",
		"-of", "csv=p=0", both).Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("ffprobe: %v", err)
	}
	return string(out)
}

// samePackets says whether two of ffprobe's lists of packets are the same
// packets at the same times, to the millisecond MKV keeps them to.
func samePackets(a, b string) bool {
	la, lb := strings.Split(strings.TrimSpace(a), "\n"), strings.Split(strings.TrimSpace(b), "\n")
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		fa, fb := strings.Split(la[i], ","), strings.Split(lb[i], ",")
		if len(fa) != 5 || len(fb) != 5 || fa[0] != fb[0] || fa[3] != fb[3] || fa[4] != fb[4] {
			return false
		}
		for _, j := range []int{1, 2} {
			x, _ := strconv.ParseFloat(fa[j], 64)
			y, _ := strconv.ParseFloat(fb[j], 64)
			if math.Abs(x-y) > 0.0015 {
				return false
			}
		}
	}
	return true
}

// A segment ffmpeg made after starting again there is the one it makes
// reading through: same init, same timestamps.
func TestRemuxStartsAgain(t *testing.T) {
	p := clip(t, 20)
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("needs ffprobe")
	}
	src := Source{Path: p, Audio: 0}
	copyOut := func(m *Manager, name string) string {
		path, err := m.File(context.Background(), src, "me", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(path)
		out := filepath.Join(t.TempDir(), name)
		os.WriteFile(out, b, 0o644)
		return out
	}

	through := newManager(t)
	pl, err := through.Playlist(src, "me", 0, "/remux/x/", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(pl), ".m4s"); n != 20 {
		t.Fatalf("%d segments:\n%s", n, pl)
	}
	init1, seg1 := copyOut(through, "init.mp4"), copyOut(through, "00015.m4s")
	through.Stop()

	again := newManager(t)
	if _, err := again.Playlist(src, "me", 15.2, "/remux/x/", ""); err != nil {
		t.Fatal(err)
	}
	init2, seg2 := copyOut(again, "init.mp4"), copyOut(again, "00015.m4s")
	if again.cur.run.from != 15 {
		t.Errorf("started at %d, not at the segment asked for", again.cur.run.from)
	}
	a, b := packets(t, init1, seg1), packets(t, init2, seg2)
	if !samePackets(a, b) {
		t.Errorf("reading through:\n%s\nstarting again:\n%s", a, b)
	}
	if !strings.HasPrefix(a, "0,15.") {
		t.Errorf("segment 15 doesn't start 15 s in:\n%s", a)
	}

	// Further on than ffmpeg will be soon: it starts again there.
	copyOut(again, "00002.m4s")
	if again.cur.run.from != 2 {
		t.Errorf("seeking back: running from %d", again.cur.run.from)
	}
	if _, err := again.File(context.Background(), src, "me", "00020.m4s"); err == nil {
		t.Error("a segment past the end")
	}
	if _, err := again.File(context.Background(), src, "me", "junk"); err == nil {
		t.Error("not a segment")
	}
}

func TestRemuxPausesAndGoesIdle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	p := clip(t, 20)
	m := newManager(t)
	m.Ahead = 3
	m.inputArgs = []string{"-readrate", "4"} // four times as fast as playing
	src := Source{Path: p, Audio: 0}
	if _, err := m.Playlist(src, "me", 0, "/", ""); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		m.cur.mu.Lock()
		pid := m.cur.run.cmd.Process.Pid
		m.cur.mu.Unlock()
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		f := strings.Fields(string(b))
		if len(f) < 3 {
			return ""
		}
		return f[2]
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal(what)
			}
		}
	}
	waitFor("ffmpeg wasn't paused", func() bool { return state() == "T" })
	m.cur.mu.Lock()
	made := m.cur.run.made
	m.cur.mu.Unlock()
	if made < 3 || made > 8 {
		t.Errorf("paused having made %d segments, for 3 s ahead", made)
	}
	// The player catching up carries it on.
	if _, err := m.File(context.Background(), src, "me", "00004.m4s"); err != nil {
		t.Fatal(err)
	}
	waitFor("ffmpeg wasn't carried on", func() bool { return state() != "T" })

	// Someone else, while it's in use.
	other := Source{Path: p, Audio: -1}
	if _, err := m.Playlist(other, "them", 0, "/", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("someone else: %v", err)
	}
	// The same viewer moving on to something else.
	if _, err := m.Playlist(other, "me", 0, "/", ""); err != nil {
		t.Errorf("moving on: %v", err)
	}
	if _, err := m.File(context.Background(), src, "me", "00004.m4s"); err == nil {
		t.Error("the first remux is still there")
	}

	// Nobody asking for anything: stopped, and its segments gone.
	idle := newManager(t)
	idle.Idle = 300 * time.Millisecond
	if _, err := idle.Playlist(src, "me", 0, "/", ""); err != nil {
		t.Fatal(err)
	}
	dir := idle.cur.dir
	waitFor("not stopped when idle", func() bool { return !idle.Running() })
	if _, err := os.Stat(dir); err == nil {
		t.Error("its segments are still there")
	}
}

func TestSegmentNumber(t *testing.T) {
	for name, want := range map[string]int{"00000.m4s": 0, "00012.m4s": 12, "123456.m4s": 123456,
		"0012.m4s": -1, "00012.mp4": -1, "init.mp4": -1, "-0001.m4s": -1, "+0001.m4s": -1, "0001a.m4s": -1} {
		n, ok := segmentNumber(name)
		if (want < 0) == ok || (ok && n != want) {
			t.Errorf("%s: %d %v", name, n, ok)
		}
	}
}

// After a seek, a request from before it is still waiting: it doesn't
// start ffmpeg back where it was.
func TestRemuxOldRequestWaits(t *testing.T) {
	p := clip(t, 60)
	m := newManager(t)
	m.inputArgs = []string{"-readrate", "1"} // as fast as playing
	src := Source{Path: p, Audio: 0}
	if _, err := m.Playlist(src, "me", 0, "/", ""); err != nil {
		t.Fatal(err)
	}
	old := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := m.File(ctx, src, "me", "00008.m4s") // eight seconds off
		old <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if _, err := m.File(context.Background(), src, "me", "00050.m4s"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	m.cur.mu.Lock()
	from := m.cur.run.from
	m.cur.mu.Unlock()
	if from != 50 {
		t.Errorf("running from %d, not the seek's 50", from)
	}
	cancel()
	if err := <-old; !errors.Is(err, context.Canceled) {
		t.Errorf("the request from before: %v", err)
	}
}

func TestRemuxNoIndex(t *testing.T) {
	m := New("ffmpeg", filepath.Join(t.TempDir(), "remux"))
	junk := filepath.Join(t.TempDir(), "junk.mkv")
	os.WriteFile(junk, mkv(true, info(6000), tracks(), cluster()), 0o644)
	if _, err := m.Playlist(Source{Path: junk}, "me", 0, "/", ""); !errors.Is(err, ErrNoIndex) {
		t.Errorf("%v", err)
	}
	if m.Running() {
		t.Error("running")
	}
}
