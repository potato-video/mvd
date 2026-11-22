//go:build !downloads

package library

import "mvd/internal/config"

// Built without downloads: there's nothing to ask about downloads.
func newDownloadClient(config.Config) downloadClient { return nil }
