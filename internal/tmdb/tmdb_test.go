package tmdb

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mvd/internal/config"
)

func TestPruneCache(t *testing.T) {
	tm := New(config.Config{CacheDir: t.TempDir()})
	tm.cachePut("old", []byte("{}"))
	tm.cachePut("new", []byte("{}"))
	past := time.Now().Add(-2 * tm.ttl)
	os.Chtimes(tm.cachePath("old"), past, past)
	tm.PruneCache()
	if _, err := os.Stat(tm.cachePath("old")); err == nil {
		t.Errorf("expired entry kept")
	}
	if _, err := os.Stat(tm.cachePath("new")); err != nil {
		t.Errorf("fresh entry removed")
	}
}

// Review L10: TMDB is a peer the owner chose, but a bad day there mustn't
// cost the router its memory or hold the import lock for an hour.
func TestTMDBLimits(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case strings.HasPrefix(r.URL.Path, "/3/movie/1"):
			if calls == 1 {
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(429)
				return
			}
			w.Write([]byte(`{"id": 1, "title": "Waited"}`))
		case strings.HasPrefix(r.URL.Path, "/3/movie/2"):
			w.Write([]byte(`{"id": 2, "title": "`))
			w.Write(bytes.Repeat([]byte("x"), maxAnswer+10))
			w.Write([]byte(`"}`))
		case strings.HasPrefix(r.URL.Path, "/t/p/"):
			w.Write(bytes.Repeat([]byte("x"), maxImage+10))
		}
	}))
	defer srv.Close()
	saved := maxRetryWait
	maxRetryWait = 50 * time.Millisecond
	defer func() { maxRetryWait = saved }()
	tm := New(config.Config{APIKey: "k"})
	tm.SetBaseURLs(srv.URL+"/3", srv.URL+"/t/p/")

	start := time.Now()
	if m, err := tm.Movie(1); err != nil || m.Title != "Waited" || time.Since(start) > 5*time.Second {
		t.Errorf("after a 429: %v %v, in %v", m, err, time.Since(start))
	}
	if _, err := tm.Movie(2); err == nil {
		t.Error("an answer past the limit was read")
	}
	dest := filepath.Join(t.TempDir(), "poster.jpg")
	if err := tm.DownloadImage("/p.jpg", "w500", dest); err == nil {
		t.Error("an image past the limit was saved")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a part of it was left")
	}
}
