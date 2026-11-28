//go:build !(remux && webui)

package server

import (
	"net/http"

	"mvd/internal/library"
)

// Built without remuxing (or without the website, its only user).

type remuxState struct{}

func (sv *Server) remuxRoutes(*http.ServeMux) {}
func (sv *Server) stopRemux()                 {}

func (sv *Server) remuxURLs(id string, media []*library.MediaFile, n int) []string {
	return make([]string, n)
}
