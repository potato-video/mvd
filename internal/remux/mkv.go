package remux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Reading an MKV's index: its Cues, the times of the video's keyframes,
// which mkvmerge and ffmpeg write near the end of the file, and which the
// SeekHead at the start points to. Nothing else of the file is read: the
// playlist lists one segment per keyframe, and ffmpeg cuts at each.

// Index is a video's keyframes, in seconds, and its length.
type Index struct {
	Keyframes []float64
	Duration  float64
}

// ErrNoIndex is a file with no Cues for its video, which can't be listed
// as segments before ffmpeg has read it all.
var ErrNoIndex = errors.New("the file has no index of its keyframes")

// Matroska's element IDs, with their length markers.
const (
	idEBML           = 0x1A45DFA3
	idSegment        = 0x18538067
	idSeekHead       = 0x114D9B74
	idSeek           = 0x4DBB
	idSeekID         = 0x53AB
	idSeekPosition   = 0x53AC
	idInfo           = 0x1549A966
	idTimestampScale = 0x2AD7B1
	idDuration       = 0x4489
	idTracks         = 0x1654AE6B
	idTrackEntry     = 0xAE
	idTrackNumber    = 0xD7
	idTrackType      = 0x83
	idCues           = 0x1C53BB6B
	idCuePoint       = 0xBB
	idCueTime        = 0xB3
	idCueTrackPos    = 0xB7
	idCueTrack       = 0xF7
	idCluster        = 0x1F43B675
)

// The most of each part read into memory: a film's Cues, with a point for
// each of a few thousand keyframes, are tens of KB.
const (
	maxHead = 1 << 20  // SeekHead, Info, Tracks
	maxCues = 16 << 20 // Cues
)

const unknownSize = -1

// ReadIndex reads an MKV's keyframes from its Cues.
func ReadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readIndex(f)
}

func readIndex(f io.ReadSeeker) (*Index, error) {
	id, size, n, err := readHeader(f)
	if err != nil || id != idEBML || size == unknownSize {
		return nil, errors.New("not a Matroska file")
	}
	if _, err := f.Seek(size, io.SeekCurrent); err != nil {
		return nil, err
	}
	pos := n + size
	id, size, n, err = readHeader(f)
	if err != nil || id != idSegment {
		return nil, errors.New("not a Matroska file: no segment")
	}
	seg := pos + n // where the segment's children, and SeekPositions, start
	end := int64(math.MaxInt64)
	if size != unknownSize {
		end = seg + size
	}

	scale := int64(1000000) // ns a tick: Matroska's default
	var duration float64
	video := uint64(0) // the first video track's number
	var cues []byte
	var at = map[uint64]int64{} // where the SeekHead says each part is
	done := map[uint64]bool{}

	read := func(pos, size, max int64) ([]byte, error) {
		if size == unknownSize || size > max {
			return nil, fmt.Errorf("an index part of %d bytes", size)
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		b := make([]byte, size)
		_, err := io.ReadFull(f, b)
		return b, err
	}
	// part reads one of the top-level elements wanted, at pos.
	part := func(id uint64, pos int64) error {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		gotID, size, n, err := readHeader(f)
		if err != nil || gotID != id {
			return fmt.Errorf("nothing at %d", pos)
		}
		max := int64(maxHead)
		if id == idCues {
			max = maxCues
		}
		b, err := read(pos+n, size, max)
		if err != nil {
			return err
		}
		done[id] = true
		switch id {
		case idSeekHead:
			for _, s := range children(b, idSeek) {
				sid, p := uint64(0), int64(-1)
				for _, e := range elements(s) {
					switch e.id {
					case idSeekID:
						sid = uintOf(e.data)
					case idSeekPosition:
						p = int64(uintOf(e.data))
					}
				}
				if p >= 0 {
					if _, ok := at[sid]; !ok {
						at[sid] = seg + p
					}
				}
			}
		case idInfo:
			for _, e := range elements(b) {
				switch e.id {
				case idTimestampScale:
					if s := int64(uintOf(e.data)); s > 0 {
						scale = s
					}
				case idDuration:
					duration = floatOf(e.data)
				}
			}
		case idTracks:
			for _, t := range children(b, idTrackEntry) {
				num, typ := uint64(0), uint64(0)
				for _, e := range elements(t) {
					switch e.id {
					case idTrackNumber:
						num = uintOf(e.data)
					case idTrackType:
						typ = uintOf(e.data)
					}
				}
				if typ == 1 && video == 0 {
					video = num
				}
			}
		case idCues:
			cues = b
		}
		return nil
	}

	// Walk the segment's top-level elements, reading the parts wanted, up
	// to the first cluster; then jump to whatever the SeekHead says is
	// after it. With no SeekHead entry for the Cues, keep walking, a
	// cluster at a time.
	for pos = seg; pos < end; {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		id, size, n, err := readHeader(f)
		if err != nil {
			break // the end of the file
		}
		switch id {
		case idSeekHead, idInfo, idTracks, idCues:
			if !done[id] {
				if err := part(id, pos); err != nil {
					return nil, err
				}
			}
		case idCluster:
			for _, want := range []uint64{idInfo, idTracks, idCues} {
				if p, ok := at[want]; ok && !done[want] {
					if err := part(want, p); err != nil {
						return nil, err
					}
				}
			}
		}
		if done[idInfo] && done[idTracks] && done[idCues] {
			break
		}
		if size == unknownSize {
			break // a cluster being written as it streams: can't go past it
		}
		pos += n + size
	}
	if video == 0 || cues == nil {
		return nil, ErrNoIndex
	}

	ix := &Index{Duration: duration * float64(scale) / 1e9}
	for _, p := range children(cues, idCuePoint) {
		t, mine := int64(-1), false
		for _, e := range elements(p) {
			switch e.id {
			case idCueTime:
				t = int64(uintOf(e.data))
			case idCueTrackPos:
				for _, c := range elements(e.data) {
					if c.id == idCueTrack && uintOf(c.data) == video {
						mine = true
					}
				}
			}
		}
		if t >= 0 && mine {
			ix.Keyframes = append(ix.Keyframes, float64(t*scale)/1e9)
		}
	}
	sort.Float64s(ix.Keyframes)
	out := ix.Keyframes[:0]
	for _, k := range ix.Keyframes {
		if len(out) == 0 || k > out[len(out)-1] {
			out = append(out, k)
		}
	}
	ix.Keyframes = out
	if len(ix.Keyframes) == 0 {
		return nil, ErrNoIndex
	}
	if last := ix.Keyframes[len(ix.Keyframes)-1]; ix.Duration <= last {
		return nil, errors.New("the file's length isn't known")
	}
	return ix, nil
}

// readHeader reads an element's ID and size, and how many bytes they took.
func readHeader(r io.Reader) (id uint64, size int64, n int64, err error) {
	var b [8]byte
	if _, err = io.ReadFull(r, b[:1]); err != nil {
		return
	}
	l := vintLen(b[0])
	if l == 0 || l > 4 {
		return 0, 0, 0, errors.New("bad element ID")
	}
	if _, err = io.ReadFull(r, b[1:l]); err != nil {
		return
	}
	id = uintOf(b[:l])
	if _, err = io.ReadFull(r, b[:1]); err != nil {
		return
	}
	sl := vintLen(b[0])
	if sl == 0 {
		return 0, 0, 0, errors.New("bad element size")
	}
	if _, err = io.ReadFull(r, b[1:sl]); err != nil {
		return
	}
	size = sizeOf(b[:sl])
	return id, size, int64(l + sl), nil
}

// vintLen is how long a variable-length integer is, from its first byte: 0
// for none.
func vintLen(b byte) int {
	for i := 0; i < 8; i++ {
		if b&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// sizeOf reads a size: the vint without its marker, all ones for unknown.
func sizeOf(b []byte) int64 {
	v := uint64(b[0] & (0xFF >> len(b)))
	all := v == uint64(0xFF>>len(b))
	for _, c := range b[1:] {
		v = v<<8 | uint64(c)
		all = all && c == 0xFF
	}
	if all || v > math.MaxInt64/2 {
		return unknownSize
	}
	return int64(v)
}

func uintOf(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func floatOf(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

type element struct {
	id   uint64
	data []byte
}

// elements splits an element's body into its children, stopping at the
// first that doesn't fit.
func elements(b []byte) []element {
	var out []element
	for len(b) > 0 {
		l := vintLen(b[0])
		if l == 0 || l > 4 || len(b) < l+1 {
			break
		}
		id := uintOf(b[:l])
		sl := vintLen(b[l])
		if sl == 0 || len(b) < l+sl {
			break
		}
		size := sizeOf(b[l : l+sl])
		if size == unknownSize || int64(len(b)-l-sl) < size {
			break
		}
		out = append(out, element{id, b[l+sl : l+sl+int(size)]})
		b = b[l+sl+int(size):]
	}
	return out
}

// children are the bodies of b's children with that ID.
func children(b []byte, id uint64) [][]byte {
	var out [][]byte
	for _, e := range elements(b) {
		if e.id == id {
			out = append(out, e.data)
		}
	}
	return out
}
