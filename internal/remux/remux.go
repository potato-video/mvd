// Package remux repackages an MKV's video and one of its audio tracks as
// HLS, in fragmented MP4 segments, for browsers that won't open MKV
// (Safari, and so iPhones and iPads). ffmpeg does it with -c copy, which
// decodes nothing, so it's light enough for the router. One remux runs at
// a time, a couple of minutes ahead of the player at most, writing to the
// cache folder on the drive.
//
// The playlist is made here, from the file's keyframes (mkv.go), so it's
// whole from the start; ffmpeg cuts a segment at every keyframe, so
// segment N always starts at keyframe N. A seek past what ffmpeg has made
// starts it again there: with the original timestamps kept (-copyts) and
// each fragment's time absolute (frag_discont), what it makes then is what
// it would have made reading through.
package remux

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Source is what to remux: a file, and which of its audio tracks (by
// position among them; -1 for none).
type Source struct {
	Path  string
	Audio int
	HEVC  bool // tagged hvc1, which Apple wants, in place of MKV's none
}

func (s Source) key() string { return s.Path + "\x00" + strconv.Itoa(s.Audio) }

// ErrBusy is a remux asked for while someone else's is running.
var ErrBusy = errors.New("another remux is running; try again when it's finished")

// Manager runs the remuxes, one at a time.
type Manager struct {
	FFmpeg string // its path
	Dir    string // where segments go: on the drive, never /tmp (RAM on OpenWRT)

	Idle   time.Duration // a remux nobody's asked anything of for this long is stopped
	Ahead  float64       // seconds ffmpeg may get ahead of the player before it's paused
	Behind float64       // seconds of segments kept behind the player
	Wait   time.Duration // how long a request waits for its segment

	mu  sync.Mutex
	cur *session

	inputArgs []string // for tests: "-readrate", to slow it down
}

// New makes a manager for ffmpeg at path, keeping its segments in dir,
// which it empties.
func New(ffmpeg, dir string) *Manager {
	os.RemoveAll(dir) // what an earlier run left
	return &Manager{FFmpeg: ffmpeg, Dir: dir, Idle: 2 * time.Minute, Ahead: 120, Behind: 120, Wait: 30 * time.Second}
}

// FFmpeg is ffmpeg's path, "" when it isn't on the PATH.
func FFmpeg() string {
	p, _ := exec.LookPath("ffmpeg")
	return p
}

// session is one source being remuxed, by one owner.
type session struct {
	m     *Manager
	src   Source
	owner string
	ix    *Index
	dir   string

	mu      sync.Mutex
	run     *run      // ffmpeg as it is now; nil when not running
	asked   int       // the segment last asked for
	asks    int       // requests so far: only the latest may start ffmpeg again
	used    time.Time // when it was last asked for anything
	failed  error     // set when ffmpeg can't do this file
	stopped bool
}

// run is one ffmpeg, from one segment on.
type run struct {
	cmd     *exec.Cmd
	from    int  // the segment it started at
	made    int  // the last segment it has finished
	paused  bool // stopped with SIGSTOP, for being far enough ahead
	exited  chan struct{}
	err     error // how it exited, once exited is closed
	killing atomic.Bool
	stderr  *tail
}

// done says whether ffmpeg has exited.
func (r *run) done() bool {
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

// Playlist starts remuxing src for owner, from t seconds in, if it isn't
// already, and returns its playlist. A remux of something else by another
// owner is ErrBusy, unless it's gone idle; one by the same owner is
// stopped, since they've moved on.
func (m *Manager) Playlist(src Source, owner string, t float64, base, query string) ([]byte, error) {
	s, err := m.session(src, owner, true)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used = time.Now()
	if s.failed != nil {
		return nil, s.failed
	}
	if s.run == nil {
		n := s.ix.Segment(t)
		s.asked = n
		if err := s.start(n); err != nil {
			return nil, err
		}
	}
	return s.ix.Playlist(base, query), nil
}

// File returns the path of init.mp4 or a segment ("00012.m4s") of src's
// remux once ffmpeg has made it, starting ffmpeg again where it's needed.
// It waits until ctx is done (the player gave up on it) at most.
func (m *Manager) File(ctx context.Context, src Source, owner, name string) (string, error) {
	s, err := m.session(src, owner, false)
	if err != nil {
		return "", err
	}
	n := -1
	if name != "init.mp4" {
		num, ok := segmentNumber(name)
		if !ok || num >= len(s.ix.Keyframes) {
			return "", os.ErrNotExist
		}
		n = num
	}
	return s.file(ctx, n)
}

// session finds src's session, making one (reading the file's index) if
// make is set.
func (m *Manager) session(src Source, owner string, make bool) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.cur; s != nil {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			m.cur = nil
		}
	}
	if s := m.cur; s != nil {
		if s.src.key() == src.key() && s.owner == owner {
			return s, nil
		}
		if !make {
			return nil, os.ErrNotExist // a playlist from before: reload it
		}
		s.mu.Lock()
		idle := time.Since(s.used) >= m.Idle
		s.mu.Unlock()
		if s.owner != owner && !idle {
			return nil, ErrBusy
		}
		s.stop()
		m.cur = nil
	}
	if !make {
		return nil, os.ErrNotExist
	}
	ix, err := ReadIndex(src.Path)
	if err != nil {
		return nil, err
	}
	s := &session{m: m, src: src, owner: owner, ix: ix, dir: filepath.Join(m.Dir, "now"), used: time.Now()}
	os.RemoveAll(s.dir)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	m.cur = s
	go s.watch()
	return s, nil
}

// Stop stops whatever's running, for the server shutting down.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		m.cur.stop()
		m.cur = nil
	}
}

// Running says whether a remux is running, for the status.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

// segmentNumber reads "00012.m4s": five digits or more, as %05d writes them.
func segmentNumber(name string) (int, bool) {
	num, ok := strings.CutSuffix(name, ".m4s")
	if !ok || len(num) < 5 {
		return 0, false
	}
	for _, c := range num {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(num)
	return n, err == nil
}

func (s *session) segPath(n int) string {
	if n < 0 {
		return filepath.Join(s.dir, "init.mp4")
	}
	return filepath.Join(s.dir, fmt.Sprintf("%05d.m4s", n))
}

// file waits for segment n (-1 for init.mp4), starting ffmpeg there if
// it isn't on its way. A request that isn't the latest only waits: after a
// seek, the player's request from before mustn't start ffmpeg back there.
func (s *session) file(ctx context.Context, n int) (string, error) {
	path := s.segPath(n)
	deadline := time.Now().Add(s.m.Wait)
	s.mu.Lock()
	s.used = time.Now()
	if n >= 0 {
		s.asked = n
	}
	s.asks++
	me := s.asks
	for {
		if s.failed != nil {
			err := s.failed
			s.mu.Unlock()
			return "", err
		}
		if s.stopped {
			s.mu.Unlock()
			return "", os.ErrNotExist
		}
		if _, err := os.Stat(path); err == nil {
			s.mu.Unlock()
			return path, nil
		}
		r := s.run
		var coming bool
		switch {
		case r == nil || r.done():
		case n < 0:
			coming = true
		default:
			// ffmpeg hasn't passed it, and will be there soon.
			at := max(r.made, r.from)
			coming = n > r.made && n >= r.from && s.ix.Keyframes[n]-s.ix.Keyframes[at] < 30
		}
		switch {
		case !coming && me == s.asks:
			start := n
			if n < 0 {
				start = s.asked
			}
			if err := s.start(start); err != nil {
				s.mu.Unlock()
				return "", err
			}
		case coming && r.paused:
			r.resume()
		}
		if time.Now().After(deadline) {
			s.mu.Unlock()
			return "", errors.New("ffmpeg is taking too long")
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		s.mu.Lock()
	}
}

// start runs ffmpeg from segment n, stopping the one running. With s.mu
// held.
func (s *session) start(n int) error {
	if s.run != nil {
		s.run.kill()
		s.run = nil
	}
	tmps, _ := filepath.Glob(filepath.Join(s.dir, "*.tmp"))
	for _, t := range tmps {
		os.Remove(t)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if n > 0 {
		args = append(args, "-seek_timestamp", "1", "-ss", strconv.FormatFloat(s.ix.seekTo(n), 'f', 3, 64))
	}
	args = append(args, s.m.inputArgs...)
	args = append(args, "-i", s.src.Path, "-map", "0:v:0")
	if s.src.Audio >= 0 {
		args = append(args, "-map", "0:a:"+strconv.Itoa(s.src.Audio))
	}
	args = append(args, "-c", "copy")
	if s.src.HEVC {
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args, "-copyts", "-avoid_negative_ts", "disabled",
		"-f", "hls", "-hls_segment_type", "fmp4", "-hls_time", "0.001", // a segment at every keyframe
		"-hls_list_size", "0", "-hls_playlist_type", "event", "-hls_flags", "temp_file",
		"-start_number", strconv.Itoa(n),
		"-hls_segment_options", "movflags=+frag_discont",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(s.dir, "%05d.m4s"),
		filepath.Join(s.dir, "ffmpeg.m3u8"))
	cmd := exec.Command(s.m.FFmpeg, args...)
	r := &run{cmd: cmd, from: n, made: n - 1, exited: make(chan struct{}), stderr: &tail{max: 4 << 10}}
	cmd.Stderr = r.stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// If memory runs out, the kernel should kill this, cheap to start
	// again, before mvd or Transmission.
	os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("1000"), 0)
	go func() {
		err := cmd.Wait()
		r.err = err
		close(r.exited)
		if err == nil || r.killing.Load() {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.run == r {
			log.Printf("mvd: remux of %s: ffmpeg: %v: %s", filepath.Base(s.src.Path), err, r.stderr.text())
			s.failed = fmt.Errorf("ffmpeg couldn't remux this file: %s", r.stderr.last())
		}
	}()
	s.run = r
	return nil
}

// watch keeps the session in step every so often: counts what ffmpeg has
// made, pauses it when it's far enough ahead, deletes segments well behind
// the player, and stops the session when it's idle.
func (s *session) watch() {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	oldest := 0
	for range t.C {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if time.Since(s.used) >= s.m.Idle {
			s.mu.Unlock()
			s.stop()
			s.m.mu.Lock()
			if s.m.cur == s {
				s.m.cur = nil
			}
			s.m.mu.Unlock()
			return
		}
		if r := s.run; r != nil {
			for r.made+1 < len(s.ix.Keyframes) {
				if _, err := os.Stat(s.segPath(r.made + 1)); err != nil {
					break
				}
				r.made++
			}
			if !r.done() && r.made >= r.from {
				ahead := s.ix.Keyframes[r.made] - s.ix.Keyframes[s.asked]
				switch {
				case !r.paused && ahead > s.m.Ahead:
					r.pause()
				case r.paused && ahead < s.m.Ahead/2:
					r.resume()
				}
			}
		}
		keep := s.ix.Segment(s.ix.Keyframes[s.asked] - s.ix.Keyframes[0] - s.m.Behind)
		for ; oldest < keep; oldest++ {
			os.Remove(s.segPath(oldest))
		}
		if keep < oldest {
			oldest = keep // a seek back
		}
		s.mu.Unlock()
	}
}

// stop kills ffmpeg and removes what it made.
func (s *session) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		s.run.kill()
		s.run = nil
	}
	s.stopped = true
	os.RemoveAll(s.dir)
}

// kill stops ffmpeg and waits for it to go.
func (r *run) kill() {
	if !r.done() {
		r.killing.Store(true)
		r.cmd.Process.Kill() // a stopped process too
		<-r.exited
	}
}

// tail keeps the last max bytes written to it: ffmpeg's last words.
type tail struct {
	b   []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) text() string { return string(t.b) }

// last is the last line written.
func (t *tail) last() string {
	b := t.b
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return string(b[i+1:])
		}
	}
	return string(b)
}
