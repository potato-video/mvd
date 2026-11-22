//go:build webui && files && !downloads

package server

import "mvd/internal/library"

// Built with the web UI but not torrents: Incoming has no downloads.

// incomingTorrents: no downloads in this build.
func (sv *Server) incomingTorrents(*incomingView, map[string]*library.ItemResult, importScan) map[string]bool {
	return nil
}
