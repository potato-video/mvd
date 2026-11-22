package testutil

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// ArchiveFile is a file to put in a test archive.
type ArchiveFile struct {
	Name   string
	Data   []byte
	Method int // 0 stored; 1 to 5 claim to be compressed (the data is still plain)
	Link   bool
	// UnknownSize marks a RAR5 entry as not saying how big it is.
	UnknownSize bool
	// Dict is a compressed RAR5 entry's dictionary: 128 KB << Dict.
	Dict int
}

// WriteRAR writes files as a RAR set (RAR5 or RAR4), with at most perVol
// bytes of data in each volume, and returns the first volume's path. Volumes
// are named a.part01.rar, or with oldNames a.rar, a.r00. Only stored data:
// enough to test a reader.
func WriteRAR(t *testing.T, dir, base string, v5, oldNames bool, perVol int, files []ArchiveFile) string {
	t.Helper()
	type part struct {
		f                       ArchiveFile
		data                    []byte
		splitBefore, splitAfter bool
	}
	var vols [][]part
	cur, room := []part(nil), perVol
	for _, f := range files {
		d, first := f.Data, true
		for {
			n := min(len(d), room)
			p := part{f: f, data: d[:n], splitBefore: !first}
			d, room, first = d[n:], room-n, false
			if len(d) > 0 {
				p.splitAfter = true
			}
			cur = append(cur, p)
			if len(d) == 0 {
				break
			}
			vols, cur, room = append(vols, cur), nil, perVol
		}
	}
	if len(cur) > 0 {
		vols = append(vols, cur)
	}
	multi := len(vols) > 1
	name := func(i int) string {
		switch {
		case !multi:
			return base + ".rar"
		case oldNames && i == 0:
			return base + ".rar"
		case oldNames:
			return fmt.Sprintf("%s.r%02d", base, i-1)
		}
		return fmt.Sprintf("%s.part%02d.rar", base, i+1)
	}
	for i, vol := range vols {
		var b bytes.Buffer
		more := i < len(vols)-1
		if v5 {
			b.Write([]byte("Rar!\x1a\x07\x01\x00"))
			af := uint64(0)
			if multi {
				af = 1
			}
			b.Write(rarBlock5(1, 0, nil, rarVints(af), 0))
			for _, p := range vol {
				flags := uint64(0x02)
				if p.splitBefore {
					flags |= 0x08
				}
				if p.splitAfter {
					flags |= 0x10
				}
				crc := crc32.ChecksumIEEE(p.f.Data)
				if p.splitAfter {
					crc = crc32.ChecksumIEEE(p.data)
				}
				attr := uint64(0o100644)
				if p.f.Link {
					attr = 0o120777
				}
				ff := uint64(0x04)
				if p.f.UnknownSize {
					ff |= 0x08
				}
				body := rarVints(ff, uint64(len(p.f.Data)), attr)
				body = binary.LittleEndian.AppendUint32(body, crc)
				body = append(body, rarVints(uint64(p.f.Method)<<7|uint64(p.f.Dict)<<10, 1, uint64(len(p.f.Name)))...)
				body = append(body, p.f.Name...)
				b.Write(rarBlock5(2, flags, body, nil, len(p.data)))
				b.Write(p.data)
			}
			ef := uint64(0)
			if more {
				ef = 1
			}
			b.Write(rarBlock5(5, 0, rarVints(ef), nil, 0))
		} else {
			b.Write([]byte("Rar!\x1a\x07\x00"))
			mf := uint16(0)
			if multi {
				mf = 0x0001
				if !oldNames {
					mf |= 0x10
				}
			}
			b.Write(rarBlock4(0x73, mf, make([]byte, 6)))
			for _, p := range vol {
				flags := uint16(0x8000)
				if p.splitBefore {
					flags |= 0x01
				}
				if p.splitAfter {
					flags |= 0x02
				}
				crc := crc32.ChecksumIEEE(p.f.Data)
				if p.splitAfter {
					crc = crc32.ChecksumIEEE(p.data)
				}
				attr := uint32(0o100644)
				if p.f.Link {
					attr = 0o120777
				}
				le := binary.LittleEndian
				var h []byte
				h = le.AppendUint32(h, uint32(len(p.data)))
				h = le.AppendUint32(h, uint32(len(p.f.Data)))
				h = append(h, 3)
				h = le.AppendUint32(h, crc)
				h = le.AppendUint32(h, 0)
				h = append(h, 29, byte(0x30+p.f.Method))
				h = le.AppendUint16(h, uint16(len(p.f.Name)))
				h = le.AppendUint32(h, attr)
				h = append(h, p.f.Name...)
				b.Write(rarBlock4(0x74, flags, h))
				b.Write(p.data)
			}
			ef := uint16(0)
			if more {
				ef = 0x0001
			}
			b.Write(rarBlock4(0x7b, ef, nil))
		}
		if err := os.WriteFile(filepath.Join(dir, name(i)), b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, name(0))
}

func rarBlock4(typ byte, flags uint16, body []byte) []byte {
	le := binary.LittleEndian
	h := []byte{typ}
	h = le.AppendUint16(h, flags)
	h = le.AppendUint16(h, uint16(7+len(body)))
	h = append(h, body...)
	return append(le.AppendUint16(nil, uint16(crc32.ChecksumIEEE(h))), h...)
}

func rarVints(vs ...uint64) []byte {
	var out []byte
	for _, v := range vs {
		for v >= 0x80 {
			out = append(out, byte(v)|0x80)
			v >>= 7
		}
		out = append(out, byte(v))
	}
	return out
}

func rarBlock5(typ, flags uint64, body, mainBody []byte, data int) []byte {
	if mainBody != nil {
		body = mainBody
	}
	h := rarVints(typ, flags)
	if data > 0 || flags&0x02 != 0 {
		h = append(h, rarVints(uint64(data))...)
	}
	h = append(h, body...)
	h = append(rarVints(uint64(len(h))), h...)
	return append(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(h)), h...)
}
