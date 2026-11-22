package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mvd/internal/config"
	"mvd/internal/library"
	"mvd/internal/tmdb"
)

func cmdImport(args []string) int {
	fs := newFlagSet("mvd import")
	var o library.Options
	fs.BoolVar(&o.DryRun, "n", false, "dry run: show what would happen, change nothing")
	yes := fs.Bool("y", false, "no prompts: file confident matches, leave the rest in place")
	fs.BoolVar(&o.Copy, "copy", false, "copy from PATH rather than moving (for importing from a stick or another share)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mvd import [flags] [PATH]\n\n"+
			"Imports everything in import_dir, or in PATH: each file or folder in it is one\n"+
			"thing to import (a movie, an episode, a season pack, a whole series...).")
		fs.PrintDefaults()
	}
	pos, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	if len(pos) > 1 {
		fs.Usage()
		return 2
	}

	cfg := config.Load()
	src := cfg.ImportDir
	if len(pos) == 1 {
		src = pos[0]
	} else if o.Copy {
		fmt.Fprintln(os.Stderr, "mvd: -copy needs a PATH to copy from")
		return 2
	}
	switch {
	case src == "":
		fmt.Fprintln(os.Stderr, "mvd: nowhere to import from: pass PATH, or set video_dir or import_dir under [paths] in ~/.config/mvd/config.toml")
		return 2
	}
	src, _ = filepath.Abs(src)
	if err := library.CheckImport(cfg, src); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 2
	}
	if len(pos) == 0 && !o.DryRun {
		os.MkdirAll(src, 0o755) // the inbox: there from the start, ready to drop things in
	}

	if !o.DryRun {
		release, err := library.LockImport(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mvd:", err)
			return 1
		}
		defer release()
	}

	tm := tmdb.New(cfg)
	tm.PruneCache()
	s := library.NewImporter(o, cfg, tm, src)
	if !*yes {
		s.SetAsk(library.AskTerminal(os.Stdin, os.Stdout))
	}
	if !o.DryRun {
		if f := library.OpenImportLog(cfg); f != nil {
			defer f.Close()
			s.SetLog(f)
		}
	}
	rc = s.Run()
	if s.Stats().Changed > 0 && !o.DryRun {
		pingServer(cfg)
	}
	return rc
}
