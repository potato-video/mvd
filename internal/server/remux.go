//go:build remux && webui

package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mvd/internal/library"
	"mvd/internal/remux"
)

// Remuxing for the website: an MKV a browser won't open (Safari's case),
// repackaged by ffmpeg as HLS, when ffmpeg is installed. See
// internal/remux. Only the website asks for one, when the browser can play
// the file's video and an audio track but not the file.

//go:embed hls/hls.light.min.js.gz
var hlsJS embed.FS

type remuxState struct {
	once sync.Once
	m    *remux.Manager // nil without ffmpeg
}

func (sv *Server) remuxer() *remux.Manager {
	sv.rx.once.Do(func() {
		if ff := remux.FFmpeg(); ff != "" && sv.cfg.CacheDir != "" {
			sv.rx.m = remux.New(ff, filepath.Join(sv.cfg.CacheDir, "remux"))
		}
	})
	return sv.rx.m
}

// stopRemux stops the remux running, if any.
func (sv *Server) stopRemux() {
	if m := sv.remuxer(); m != nil {
		m.Stop()
	}
}

func (sv *Server) remuxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /remux/hls.js", func(w http.ResponseWriter, r *http.Request) {
		b, _ := hlsJS.ReadFile("hls/hls.light.min.js.gz")
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Header().Set("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gunzipTo(w, b)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(b)
	})
	mux.HandleFunc("GET /remux/{id}/{file}", sv.remuxFile)
}

// gunzipTo writes b unzipped, for the odd client that won't take gzip.
func gunzipTo(w io.Writer, b []byte) {
	if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
		io.Copy(w, zr)
	}
}

// remuxable says whether ffmpeg can make HLS of a file that a browser
// could then play: an MKV (or WebM) with H.264, HEVC or AV1, whose audio,
// if any, fMP4 can carry. Which audio track, and whether the browser
// plays it, is the page's to ask.
func remuxable(mf *library.MediaFile) bool {
	if !mf.OK() || (mf.Container != "mkv" && mf.Container != "webm") {
		return false
	}
	for _, t := range mf.Tracks {
		if t.Kind == "video" {
			return t.Codec == "h264" || t.Codec == "hevc" || t.Codec == "av1"
		}
	}
	return false
}

// remuxURLs is each part's remux playlist for the watch page, "" for one
// that can't be remuxed: all "" without ffmpeg.
func (sv *Server) remuxURLs(id string, media []*library.MediaFile, n int) []string {
	out := make([]string, n)
	if sv.remuxer() == nil {
		return out
	}
	for i := range out {
		if i < len(media) && remuxable(media[i]) {
			out[i] = fmt.Sprintf("/remux/%s/index.m3u8?part=%d", id, i+1)
		}
	}
	return out
}

// remuxFile answers for a remux's playlist, init.mp4 and segments:
// ?part= which file of a movie (from 1), &audio= which audio track (by
// position among them, -1 for none), and for the playlist &start= where
// the player starts, in seconds.
func (sv *Server) remuxFile(w http.ResponseWriter, r *http.Request) {
	m := sv.remuxer()
	ix := sv.pageShelf(r)
	id, name := r.PathValue("id"), r.PathValue("file")
	q := r.URL.Query()
	part := max(library.Atoi(q.Get("part")), 1)
	var path string
	var mf *library.MediaFile
	if mv := ix.Movie(id); mv != nil && part <= len(mv.Files) {
		path = mv.Files[part-1]
		if part <= len(mv.Media) {
			mf = mv.Media[part-1]
		}
	} else if e := ix.Episode(id); e != nil && part == 1 {
		path, mf = e.File, e.Media
	}
	if path == "" || m == nil || !remuxable(mf) {
		http.NotFound(w, r)
		return
	}
	audio, err := strconv.Atoi(q.Get("audio"))
	if err != nil {
		audio = -1
	}
	src := remux.Source{Path: path, Audio: -1}
	video, n := false, 0
	for _, t := range mf.Tracks {
		switch {
		case t.Kind == "video" && !video:
			video, src.HEVC = true, t.Codec == "hevc"
		case t.Kind == "audio":
			if n == audio {
				src.Audio = audio
			}
			n++
		}
	}
	owner := sv.who(r).name + " " + sv.clientAddr(r).String()

	if name == "index.m3u8" {
		start, _ := strconv.ParseFloat(q.Get("start"), 64)
		query := fmt.Sprintf("?part=%d&audio=%d", part, src.Audio)
		pl, err := m.Playlist(src, owner, max(start, 0), "/remux/"+id+"/", query)
		if err != nil {
			remuxError(w, err, path)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(pl)
		return
	}
	f, err := m.File(r.Context(), src, owner, name)
	if err != nil {
		remuxError(w, err, path)
		return
	}
	sendFile(w, r, f, "video/mp4", "no-store")
}

func remuxError(w http.ResponseWriter, err error, path string) {
	switch {
	case errors.Is(err, remux.ErrBusy):
		http.Error(w, "Someone else is watching something this server has to repackage, and it does one at a time.", http.StatusConflict)
	case errors.Is(err, remux.ErrNoIndex):
		http.Error(w, "This file has no index of its keyframes, so it can't be repackaged for this browser.", http.StatusUnprocessableEntity)
	case errors.Is(err, context.Canceled):
		// The player gave up on it.
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "That's from a remux that's over; reload the page.", http.StatusNotFound)
	default:
		log.Printf("mvd: remux of %s: %v", filepath.Base(path), err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
