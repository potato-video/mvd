// Package testutil has fakes (TMDB, Transmission) and file helpers for tests.
package testutil

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type Lib struct {
	Video, Import, Movies, TV, Trash, Data string
}

func NewLib(t *testing.T) Lib {
	v := t.TempDir()
	return Lib{Video: v, Import: filepath.Join(v, "import"), Movies: filepath.Join(v, "movies"),
		TV: filepath.Join(v, "tv"), Trash: filepath.Join(v, ".trash"), Data: t.TempDir()}
}

func MustExist(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if !exists(filepath.Join(root, p)) {
			t.Errorf("missing %s", p)
		}
	}
}

func MustNotExist(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if exists(filepath.Join(root, p)) {
			t.Errorf("%s should have gone", p)
		}
	}
}

func Write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func Tree(root string) []string {
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if p != root {
			r, _ := filepath.Rel(root, p)
			if info.IsDir() {
				r += "/"
			}
			out = append(out, r)
		}
		return nil
	})
	sort.Strings(out)
	return out
}
