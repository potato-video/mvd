package library

import (
	"os"
	"path/filepath"
	"testing"
)

// Review L5: an atomic write syncs the data before the rename, or a power
// cut can leave the new name on an empty or partial file.
func TestWriteFileAtomicSyncs(t *testing.T) {
	var synced []string
	saved := syncFile
	defer func() { syncFile = saved }()
	syncFile = func(f *os.File) error {
		synced = append(synced, f.Name())
		return f.Sync()
	}
	p := filepath.Join(t.TempDir(), "state.json")
	if err := WriteFileAtomic(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "{}" {
		t.Errorf("wrote %q", b)
	}
	if len(synced) == 0 || synced[0] != p+".mvdtmp" {
		t.Errorf("synced %v", synced)
	}
	if Exists(p + ".mvdtmp") {
		t.Error("the temporary file was left")
	}

	// A copy (a move across drives, before the source is removed) too.
	synced = nil
	dst := filepath.Join(t.TempDir(), "copy.json")
	if err := copyFile(p, dst, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(synced) == 0 || synced[0] != dst+".mvdtmp" {
		t.Errorf("copy synced %v", synced)
	}
}
