//go:build !webui

package server

import "net/http"

// Built without the web UI: import, refresh, the API and streaming, without
// the web pages and the html/template machinery that comes with them.

type uiTemplates struct{}

func (sv *Server) uiRoutes(*http.ServeMux) {}

func (sv *Server) uiDownloadRoutes(*http.ServeMux) {}

func isPage(string) bool { return false }

// pairPagePath: there's no Pair page, so POST /api/pair says so, empty.
const pairPagePath = ""
