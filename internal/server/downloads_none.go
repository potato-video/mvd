//go:build !downloads

package server

import (
	"net/http"

	"mvd/internal/library"
)

// Built without downloads: no downloads on the Incoming page, and no API.

type downloadsState struct{}

func (sv *Server) hasDownloads() bool { return false }

// importDownloads: none to ask about (an import says if [downloads] is set
// up all the same).
func (sv *Server) importDownloads() library.Downloads {
	return library.LoadDownloads(sv.cfg, sv.cfg.ImportDir)
}

func (sv *Server) downloadRoutes(*http.ServeMux) {}
