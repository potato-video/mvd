//go:build !files

package server

import (
	"net/http"
	"time"
)

// Built without the files feature: no uploads (/api/uploads answers 404),
// and no Needs you API. mvd import at a terminal asks its own questions.

const uploadsBuilt = false

func (sv *Server) uploadRoutes(*http.ServeMux) {}

func (sv *Server) uploadsGoing() int { return 0 }

func (sv *Server) sweepUploadsEvery(time.Duration, <-chan struct{}) {}

func (sv *Server) needsRoutes(*http.ServeMux) {}
