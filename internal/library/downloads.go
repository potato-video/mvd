package library

import (
	"errors"
	"path/filepath"

	"mvd/internal/config"
)

// Downloads is what a download manager (Transmission) says about the items
// in an import folder: which are its downloads, and whether each is ready to
// take, so they're left alone until they've finished seeding.
type Downloads struct {
	client downloadClient
	byItem map[string]*download // by the import item each becomes
	Err    error                // Transmission is set up, but couldn't be asked
}

// errNoTorrents: [downloads] is set up, in an mvd built without downloads.
var errNoTorrents = errors.New("this mvd was built without downloads, so [downloads] in config.toml is ignored")

// LoadDownloads asks Transmission, when it's set up, about the downloads in
// src. When it can't be reached, Err says so, and import carries on without
// it: Transmission would complain about the missing files of anything taken
// mid-seed, but nothing is lost.
func LoadDownloads(cfg config.Config, src string) Downloads {
	return loadDownloads(newDownloadClient(cfg), cfg, src)
}

func loadDownloads(c downloadClient, cfg config.Config, src string) Downloads {
	d := Downloads{client: c, byItem: map[string]*download{}}
	switch {
	case cfg.Transmission == "":
		return d
	case c == nil:
		d.Err = errNoTorrents
		return d
	}
	ds, err := c.list()
	if err != nil {
		d.Err = err
		return d
	}
	for i := range ds {
		if x := &ds[i]; Within(src, x.path) {
			d.byItem[filepath.Clean(x.path)] = x
		}
	}
	return d
}

// download is what import needs to know about a download: where it lands,
// and whether it's ready to take.
type download struct {
	id       int
	path     string // the import item it becomes
	complete bool   // all downloaded (it may still be seeding)
	ready    bool   // complete, and done seeding
	why      string // if not ready, why not
	name     string
}

// downloadClient is a download manager, as import sees it.
type downloadClient interface {
	list() ([]download, error)
	forget(id int) error // take it out of the client, keeping its files
}

// loadDownloads asks Transmission, unless the import was given what it
// says already, or it's copying (the source isn't Transmission's).
func (s *Importer) loadDownloads() {
	switch {
	case s.opt.Copy:
		s.dl = &Downloads{byItem: map[string]*download{}}
		return
	case s.dl == nil:
		d := LoadDownloads(s.cfg, s.imp.src)
		s.dl = &d
	}
	switch err := s.dl.Err; {
	case err == errNoTorrents:
		s.printf("! %v\n", err)
	case err != nil:
		s.printf("! %v\n  carrying on without it: anything still seeding will be imported anyway\n", err)
	}
}

// item is the download that becomes an import item, if it's one.
func (d *Downloads) item(path string) *download {
	if d == nil {
		return nil
	}
	return d.byItem[filepath.Clean(path)]
}
