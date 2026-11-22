//go:build webui && downloads && !files

package server

import "net/http"

// Built with downloads and the web UI but not the files feature: the
// downloads' page is Incoming, which comes with files, so none here.
// (/api/downloads and mvd torrent still work.)
func (sv *Server) uiDownloadRoutes(*http.ServeMux) {}
