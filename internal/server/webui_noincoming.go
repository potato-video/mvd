//go:build webui && !files

package server

import "net/http"

// Built with the web UI but not the files feature: no Incoming page.

const incomingBuilt = false

func (sv *Server) uiIncomingRoutes(*http.ServeMux) {}

func (sv *Server) needsCount() int { return 0 }
