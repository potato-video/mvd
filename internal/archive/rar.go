package archive

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"unicode/utf16"
)

// A RAR reader for stored data only, in RAR4 (RAR 1.5 to 4.x) and RAR5
// formats. Headers are read into one small slice; data is read straight
// from the volume files into the caller's buffer. Formats:
// https://www.rarlab.com/technote.htm (RAR5), and unrar's source for RAR4.

var (
	sigRAR4 = []byte("Rar!\x1a\x07\x00")
	sigRAR5 = []byte("Rar!\x1a\x07\x01\x00")
)

// maxHeader is the largest header read: RAR5's own limit is 2 MB, and real
// headers are a few hundred bytes.
const maxHeader = 2 << 20

// rarFile is one file header: an entry, or one volume's part of it.
type rarFile struct {
	Entry
	dataOff     int64
	packed      int64 // its data in this volume
	crc         uint32
	hasCRC      bool // RAR5 may carry a BLAKE2 hash instead, which isn't checked
	partCRC     bool // crc is this part's, not the whole file's (when split after)
	splitBefore bool
	splitAfter  bool
}

// rarBlock is what nextBlock found.
type rarBlock struct {
	file       *rarFile // a file header, or nil
	end        bool     // the end-of-archive block
	nextVolume bool     // at the end: more volumes follow
}

type rarWalker struct {
	path   string
	f      *os.File
	v5     bool
	volume bool // the main header says it's one volume of several
	off    int64
	hdr    []byte
}

func walkRAR(path string, fn func(*Entry, io.Reader) error) error {
	w := &rarWalker{}
	if err := w.open(path); err != nil {
		return err
	}
	defer func() { w.f.Close() }()
	for {
		b, err := w.nextBlock()
		if err == io.EOF {
			// No end block (old archivers): a volume may still go on.
			if !w.volume || !exists(nextVolume(w.path)) {
				return nil
			}
			b = rarBlock{end: true, nextVolume: true}
		} else if err != nil {
			return err
		}
		if b.end {
			if !b.nextVolume {
				return nil
			}
			next := nextVolume(w.path)
			if !exists(next) {
				return nil // the last volume we have; nothing was cut off
			}
			if err := w.open(next); err != nil {
				return err
			}
			continue
		}
		if b.file == nil {
			continue
		}
		if b.file.splitBefore {
			return fmt.Errorf("%s: starts partway through a file; it isn't the first volume", filepath.Base(w.path))
		}
		d := &rarData{w: w, part: b.file}
		var r io.Reader = d
		switch e := &b.file.Entry; {
		case e.Encrypted:
			r = errReader{ErrEncrypted}
		case e.Dir || e.Link:
			r = errReader{errNoData}
		case !e.Stored:
			r = errReader{ErrCompressed}
		}
		if err := fn(&b.file.Entry, r); err != nil {
			return err
		}
		if err := d.skip(); err != nil {
			return err
		}
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return p != "" && err == nil
}

// open starts reading a volume, after its signature and main header.
func (w *rarWalker) open(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if w.f != nil {
		w.f.Close()
	}
	w.f, w.path, w.volume = f, path, false
	sig := make([]byte, len(sigRAR5))
	n, _ := io.ReadFull(f, sig)
	switch {
	case n >= len(sigRAR5) && string(sig) == string(sigRAR5):
		w.v5, w.off = true, int64(len(sigRAR5))
	case n >= len(sigRAR4) && string(sig[:len(sigRAR4)]) == string(sigRAR4):
		w.v5, w.off = false, int64(len(sigRAR4))
	default:
		return fmt.Errorf("%s: not a RAR archive (or one older than RAR 1.5)", filepath.Base(path))
	}
	return nil
}

// read reads n bytes of header at off into w.hdr.
func (w *rarWalker) read(off int64, n int) ([]byte, error) {
	if n > maxHeader {
		return nil, w.damaged("a header too big to be real")
	}
	if cap(w.hdr) < n {
		w.hdr = make([]byte, n)
	}
	b := w.hdr[:n]
	if _, err := w.f.ReadAt(b, off); err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

func (w *rarWalker) damaged(what string) error {
	return fmt.Errorf("%s: %s at byte %d: the archive is damaged", filepath.Base(w.path), what, w.off)
}

// nextBlock reads the header at w.off and moves past it and its data.
// io.EOF means the volume ended cleanly, between blocks.
func (w *rarWalker) nextBlock() (rarBlock, error) {
	if st, err := w.f.Stat(); err == nil && w.off >= st.Size() {
		return rarBlock{}, io.EOF
	}
	var b rarBlock
	var size, data int64
	var err error
	if w.v5 {
		b, size, data, err = w.block5()
	} else {
		b, size, data, err = w.block4()
	}
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return b, w.damaged("cut off")
		}
		return b, err
	}
	// RAR4's 64-bit sizes are two halves, so can come out negative: one
	// that moved the walk back, or nowhere, would read the same blocks
	// forever.
	if size <= 0 || data < 0 || data > maxRARSize {
		return b, w.damaged("a bad size")
	}
	if b.file != nil {
		b.file.dataOff = w.off + size
		b.file.packed = data
		b.file.Packed = -1
		if !b.file.splitBefore && !b.file.splitAfter {
			b.file.Packed = data
		}
	}
	w.off += size + data
	return b, nil
}

// maxRARSize is the most any size in a header may be: past it, sums of
// offsets could overflow.
const maxRARSize = 1 << 62

// --- RAR4 ---

const (
	head4Main = 0x73
	head4File = 0x74
	head4End  = 0x7b

	main4Volume    = 0x0001
	main4Encrypted = 0x0080 // headers encrypted
	end4NextVolume = 0x0001

	file4SplitBefore = 0x01
	file4SplitAfter  = 0x02
	file4Encrypted   = 0x04
	file4Dir         = 0xe0
	file4Large       = 0x100
	file4Unicode     = 0x200
	long4Block       = 0x8000
)

func (w *rarWalker) block4() (rarBlock, int64, int64, error) {
	var b rarBlock
	h, err := w.read(w.off, 7)
	if err != nil {
		return b, 0, 0, err
	}
	crc := binary.LittleEndian.Uint16(h)
	typ := h[2]
	flags := binary.LittleEndian.Uint16(h[3:])
	size := int(binary.LittleEndian.Uint16(h[5:]))
	if size < 7 {
		return b, 0, 0, w.damaged("a bad header")
	}
	if h, err = w.read(w.off, size); err != nil {
		return b, 0, 0, err
	}
	// Only file headers are held to their CRC: RAR 1.5 and 2.0 worked out
	// some others' (with comments, say) differently.
	if typ == head4File && uint16(crc32.ChecksumIEEE(h[2:])) != crc {
		return b, 0, 0, w.damaged("a header that fails its checksum")
	}
	var data int64
	if flags&long4Block != 0 && size >= 11 {
		data = int64(binary.LittleEndian.Uint32(h[7:]))
	}
	switch typ {
	case head4Main:
		if flags&main4Encrypted != 0 {
			return b, 0, 0, fmt.Errorf("%s: %w (its file list needs a password)", filepath.Base(w.path), ErrEncrypted)
		}
		w.volume = flags&main4Volume != 0
	case head4End:
		b.end, b.nextVolume = true, flags&end4NextVolume != 0
	case head4File:
		f, err := w.file4(h, flags)
		if err != nil {
			return b, 0, 0, err
		}
		b.file, data = f, f.packed
	case 0x7a: // a service header (comment, quick open): laid out like a file's
		if size >= 32 {
			data = int64(binary.LittleEndian.Uint32(h[7:]))
			if flags&file4Large != 0 && size >= 40 {
				data |= int64(binary.LittleEndian.Uint32(h[32:])) << 32
			}
		}
	}
	return b, int64(size), data, nil
}

func (w *rarWalker) file4(h []byte, flags uint16) (*rarFile, error) {
	if len(h) < 32 {
		return nil, w.damaged("a file header too short")
	}
	le := binary.LittleEndian
	packed := int64(le.Uint32(h[7:]))
	size := int64(le.Uint32(h[11:]))
	host := h[15]
	crc := le.Uint32(h[16:])
	unpVer := h[24]
	method := h[25]
	nameLen := int(le.Uint16(h[26:]))
	attr := le.Uint32(h[28:])
	p := 32
	if flags&file4Large != 0 {
		if len(h) < 40 {
			return nil, w.damaged("a file header too short")
		}
		packed |= int64(le.Uint32(h[32:])) << 32
		size |= int64(le.Uint32(h[36:])) << 32
		p = 40
		if size < 0 || size > maxRARSize {
			return nil, w.damaged("a bad size")
		}
	}
	if p+nameLen > len(h) {
		return nil, w.damaged("a file name past its header")
	}
	raw := h[p : p+nameLen]
	name := string(raw)
	if flags&file4Unicode != 0 {
		name = decodeName4(raw)
	}
	f := &rarFile{
		Entry: Entry{
			Name:      name,
			Size:      size,
			Dir:       flags&file4Dir == file4Dir,
			Link:      (host == 3 && attr&0xf000 == 0xa000) || ((host == 0 || host == 2) && attr&0x400 != 0),
			Encrypted: flags&file4Encrypted != 0,
			Stored:    method == 0x30,
		},
		packed:      packed,
		crc:         crc,
		hasCRC:      true,
		splitBefore: flags&file4SplitBefore != 0,
		splitAfter:  flags&file4SplitAfter != 0,
	}
	// Before the last part, the CRC is that part's (from RAR 2.0 on; 0xffffffff
	// when there's none).
	if f.splitAfter {
		f.partCRC = true
		f.hasCRC = unpVer >= 20 && crc != 0xffffffff
	}
	return f, nil
}

// decodeName4 reads RAR4's Unicode file names: the plain name, a zero byte,
// then UTF-16 coded against it.
func decodeName4(raw []byte) string {
	zero := -1
	for i, c := range raw {
		if c == 0 {
			zero = i
			break
		}
	}
	if zero < 0 {
		return string(raw) // UTF-8 already
	}
	name, enc := raw[:zero], raw[zero+1:]
	if len(enc) == 0 {
		return string(name)
	}
	var out []uint16
	high := uint16(enc[0])
	pos := 1
	var flags byte
	bits := 0
	at := func(i int) byte {
		if i < len(enc) {
			return enc[i]
		}
		return 0
	}
	for pos < len(enc) {
		if bits == 0 {
			flags, bits = enc[pos], 8
			pos++
		}
		switch flags >> 6 {
		case 0:
			out = append(out, uint16(at(pos)))
			pos++
		case 1:
			out = append(out, uint16(at(pos))|high<<8)
			pos++
		case 2:
			out = append(out, uint16(at(pos))|uint16(at(pos+1))<<8)
			pos += 2
		case 3:
			n := int(at(pos))
			pos++
			if n&0x80 != 0 {
				corr := at(pos)
				pos++
				for n = n&0x7f + 2; n > 0 && len(out) < len(name); n-- {
					out = append(out, uint16(name[len(out)]+corr)|high<<8)
				}
			} else {
				for n += 2; n > 0 && len(out) < len(name); n-- {
					out = append(out, uint16(name[len(out)]))
				}
			}
		}
		flags <<= 2
		bits -= 2
	}
	return string(utf16.Decode(out))
}

// --- RAR5 ---

const (
	head5Main       = 1
	head5File       = 2
	head5Encryption = 4
	head5End        = 5

	block5Extra       = 0x01
	block5Data        = 0x02
	block5SplitBefore = 0x08
	block5SplitAfter  = 0x10

	main5Volume    = 0x01
	end5NextVolume = 0x01

	file5Dir         = 0x01
	file5Time        = 0x02
	file5CRC         = 0x04
	file5UnknownSize = 0x08

	extra5Encryption = 1
	extra5Redirect   = 5
)

// vint reads RAR5's variable-length integer: seven bits a byte, low first.
func vint(b []byte, p *int) (uint64, bool) {
	var v uint64
	for i := 0; i < 10; i++ {
		if *p >= len(b) {
			return 0, false
		}
		c := b[*p]
		*p++
		v |= uint64(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			return v, true
		}
	}
	return 0, false
}

func (w *rarWalker) block5() (rarBlock, int64, int64, error) {
	var b rarBlock
	// The CRC, then the header's size: at most three bytes for 2 MB.
	h, err := w.read(w.off, 7)
	if err != nil {
		return b, 0, 0, err
	}
	crc := binary.LittleEndian.Uint32(h)
	p := 4
	hsize, ok := vint(h, &p)
	if !ok || hsize == 0 || hsize > maxHeader {
		return b, 0, 0, w.damaged("a bad header")
	}
	total := p + int(hsize)
	if h, err = w.read(w.off, total); err != nil {
		return b, 0, 0, err
	}
	if crc32.ChecksumIEEE(h[4:]) != crc {
		return b, 0, 0, w.damaged("a header that fails its checksum")
	}
	bad := func() (rarBlock, int64, int64, error) { return b, 0, 0, w.damaged("a bad header") }
	typ, ok1 := vint(h, &p)
	flags, ok2 := vint(h, &p)
	if !ok1 || !ok2 {
		return bad()
	}
	var extra, data uint64
	if flags&block5Extra != 0 {
		if extra, ok = vint(h, &p); !ok || extra > uint64(total) {
			return bad()
		}
	}
	if flags&block5Data != 0 {
		if data, ok = vint(h, &p); !ok || data > maxRARSize {
			return bad()
		}
	}
	body := h[:total-int(extra)]
	if p > len(body) {
		return bad()
	}
	switch typ {
	case head5Main:
		af, ok := vint(body, &p)
		if !ok {
			return bad()
		}
		w.volume = af&main5Volume != 0
	case head5Encryption:
		return b, 0, 0, fmt.Errorf("%s: %w (its file list needs a password)", filepath.Base(w.path), ErrEncrypted)
	case head5End:
		ef, _ := vint(body, &p)
		b.end, b.nextVolume = true, ef&end5NextVolume != 0
	case head5File:
		f, ok := file5(body, p, h[total-int(extra):])
		if !ok {
			return bad()
		}
		f.splitBefore = flags&block5SplitBefore != 0
		f.splitAfter = flags&block5SplitAfter != 0
		f.partCRC = f.splitAfter
		b.file = f
	}
	return b, int64(total), int64(data), nil
}

func file5(h []byte, p int, extra []byte) (*rarFile, bool) {
	ff, ok1 := vint(h, &p)
	size, ok2 := vint(h, &p)
	attr, ok3 := vint(h, &p)
	if !ok1 || !ok2 || !ok3 {
		return nil, false
	}
	if ff&file5Time != 0 {
		p += 4
	}
	f := &rarFile{}
	if ff&file5CRC != 0 {
		if p+4 > len(h) {
			return nil, false
		}
		f.crc, f.hasCRC = binary.LittleEndian.Uint32(h[p:]), true
		p += 4
	}
	comp, ok1 := vint(h, &p)
	host, ok2 := vint(h, &p)
	nameLen, ok3 := vint(h, &p)
	if !ok1 || !ok2 || !ok3 || uint64(p)+nameLen > uint64(len(h)) {
		return nil, false
	}
	f.Name = string(h[p : p+int(nameLen)])
	f.Size = int64(size)
	if ff&file5UnknownSize != 0 || size > maxRARSize {
		f.Size = -1
	}
	f.Dir = ff&file5Dir != 0
	f.Stored = (comp>>7)&7 == 0
	if !f.Stored {
		// 128 KB << n: four bits in RAR5, five in RAR7, which also adds a
		// fraction of the next size up (bits 15 to 19), left out here.
		f.Dict = 128 << 10 << min((comp>>10)&0x1f, 40)
	}
	f.Link = (host == 1 && attr&0xf000 == 0xa000) || (host == 0 && attr&0x400 != 0)
	// The extra area: records of size, type, data.
	for q := 0; q < len(extra); {
		rsize, ok := vint(extra, &q)
		if !ok || rsize == 0 || uint64(q)+rsize > uint64(len(extra)) {
			break
		}
		next := q + int(rsize)
		switch typ, _ := vint(extra, &q); typ {
		case extra5Encryption:
			f.Encrypted = true
		case extra5Redirect:
			f.Link = true
		}
		q = next
	}
	return f, true
}

// --- reading an entry's data ---

// rarData reads one entry's data, a part per volume, checking the CRCs:
// before the last part, each part's own; at the end, the whole file's.
type rarData struct {
	w     *rarWalker
	part  *rarFile
	read  int64  // of this part
	crc   uint32 // of this part so far
	whole uint32 // of the whole entry so far
	done  bool
}

func (d *rarData) Read(b []byte) (int, error) {
	for !d.done && d.read >= d.part.packed {
		if err := d.endPart(true); err != nil {
			return 0, err
		}
	}
	if d.done {
		return 0, io.EOF
	}
	if left := d.part.packed - d.read; int64(len(b)) > left {
		b = b[:left]
	}
	n, err := d.w.f.ReadAt(b, d.part.dataOff+d.read)
	d.read += int64(n)
	d.crc = crc32.Update(d.crc, crc32.IEEETable, b[:n])
	d.whole = crc32.Update(d.whole, crc32.IEEETable, b[:n])
	if err == io.EOF && n < len(b) {
		return n, d.w.damaged("cut off")
	}
	if err == io.EOF {
		err = nil
	}
	return n, err
}

// endPart finishes the current part: checks its CRC (when reading), and
// moves on to its continuation in the next volume, if it has one.
func (d *rarData) endPart(check bool) error {
	p := d.part
	if check && p.hasCRC {
		got := d.whole
		if p.partCRC {
			got = d.crc
		}
		if got != p.crc {
			return fmt.Errorf("%s: %s: %w", filepath.Base(d.w.path), p.Name, ErrChecksum)
		}
	}
	if !p.splitAfter {
		d.done = true
		return nil
	}
	d.crc = 0
	name := p.Name
	next := nextVolume(d.w.path)
	if !exists(next) {
		return fmt.Errorf("%s: continues in %s, which is missing", filepath.Base(d.w.path), filepath.Base(next))
	}
	if err := d.w.open(next); err != nil {
		return err
	}
	for {
		b, err := d.w.nextBlock()
		if err == io.EOF || (err == nil && b.end) {
			return fmt.Errorf("%s: %s should continue here, but doesn't", filepath.Base(next), name)
		}
		if err != nil {
			return err
		}
		if b.file == nil {
			continue
		}
		if !b.file.splitBefore || b.file.Name != name {
			return fmt.Errorf("%s: %s should continue here, but %s starts instead", filepath.Base(next), name, b.file.Name)
		}
		d.part, d.read = b.file, 0
		return nil
	}
}

// skip moves past whatever's left of the entry, through later volumes,
// without reading it.
func (d *rarData) skip() error {
	for !d.done {
		d.read = d.part.packed
		if err := d.endPart(false); err != nil {
			return err
		}
	}
	return nil
}
