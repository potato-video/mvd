//go:build downloads

package library

import (
	"path/filepath"

	"mvd/internal/config"
	"mvd/internal/torrent"
)

// The torrent side of import: Transmission, as a downloadClient. (Built only
// with downloads.)

func newDownloadClient(cfg config.Config) downloadClient {
	if cfg.Transmission == "" {
		return nil
	}
	return transmissionDownloads{torrent.New(cfg), cfg.SeedRatio}
}

type transmissionDownloads struct {
	c     *torrent.Client
	ratio float64
}

func (t transmissionDownloads) list() ([]download, error) {
	ts, err := t.c.Torrents()
	if err != nil {
		return nil, err
	}
	var out []download
	for _, x := range ts {
		if x.Name == "" {
			continue
		}
		ok, why := x.Ready(t.ratio)
		out = append(out, download{id: x.ID, name: x.Name, path: filepath.Join(x.DownloadDir, x.Name),
			complete: x.PercentDone >= 1, ready: ok, why: why})
	}
	return out, nil
}

func (t transmissionDownloads) forget(id int) error { return t.c.Remove(id) }

// TransmissionDownloads is LoadDownloads through a client already made (the
// server's).
func TransmissionDownloads(c *torrent.Client, cfg config.Config, src string) Downloads {
	var dc downloadClient
	if c != nil {
		dc = transmissionDownloads{c, cfg.SeedRatio}
	}
	return loadDownloads(dc, cfg, src)
}
