//go:build remux && webui

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"mvd/internal/library"
	"mvd/internal/remux"
)

// The test library's real clip, remuxed: its playlist, init.mp4 and
// segment, and the watch page offering it.
func TestRemuxRoutes(t *testing.T) {
	if library.FFprobe() == "" || remux.FFmpeg() == "" {
		t.Skip("needs ffmpeg and ffprobe")
	}
	ts := newTokenServer(t)
	defer ts.hs.Close()
	const ep = "e95396102"
	get := func(path, tok string) (int, string, http.Header) {
		resp := ts.do("GET", path, "", bearer(tok))
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	code, h, _ := get("/watch/"+ep, adminTok)
	if code != 200 || !strings.Contains(h, `data-remux="[&#34;/remux/`+ep+`/index.m3u8?part=1&#34;]"`) {
		t.Errorf("the watch page doesn't offer the remux: %d %s", code, h)
	}
	if _, h, _ := get("/watch/m238", adminTok); !strings.Contains(h, `data-remux="[&#34;&#34;]"`) {
		t.Errorf("a file that isn't a video, offered: %s", h)
	}

	code, pl, hdr := get("/remux/"+ep+"/index.m3u8?part=1&audio=1&start=0", adminTok)
	if code != 200 || hdr.Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		!strings.Contains(pl, `#EXT-X-MAP:URI="/remux/`+ep+`/init.mp4?part=1&audio=1"`) ||
		!strings.Contains(pl, "/remux/"+ep+"/00000.m4s?part=1&audio=1\n") {
		t.Fatalf("playlist: %d %s", code, pl)
	}
	for _, f := range []string{"init.mp4", "00000.m4s"} {
		code, b, hdr := get("/remux/"+ep+"/"+f+"?part=1&audio=1", adminTok)
		if code != 200 || hdr.Get("Content-Type") != "video/mp4" || len(b) < 100 {
			t.Errorf("%s: %d %s %d bytes", f, code, hdr.Get("Content-Type"), len(b))
		}
	}
	if code, _, _ := get("/remux/"+ep+"/00001.m4s?part=1&audio=1", adminTok); code != 404 {
		t.Errorf("a segment past the end: %d", code)
	}
	// Another track is another remux; nothing's been asked of it yet.
	if code, _, _ := get("/remux/"+ep+"/00000.m4s?part=1&audio=0", adminTok); code != 404 {
		t.Errorf("a segment of a remux that isn't running: %d", code)
	}
	// Someone else, while it's running.
	if code, b, _ := get("/remux/"+ep+"/index.m3u8?part=1&audio=0", denTok); code != 409 || !strings.Contains(b, "one at a time") {
		t.Errorf("someone else: %d %s", code, b)
	}
	if code, _, _ := get("/remux/m238/index.m3u8?part=1&audio=0", adminTok); code != 404 {
		t.Errorf("not a video: %d", code)
	}

	resp := ts.do("GET", "/remux/hls.js", "", map[string]string{"Authorization": "Bearer " + adminTok, "Accept-Encoding": "gzip"})
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("hls.js: %d %v", resp.StatusCode, resp.Header)
	}
	if code, js, _ := get("/remux/hls.js", adminTok); code != 200 || !strings.HasPrefix(js, "!function") {
		t.Errorf("hls.js, unzipped by the client: %d %.40q", code, js)
	}
}
