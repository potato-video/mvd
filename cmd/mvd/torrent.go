//go:build downloads

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"mvd/internal/config"
	"mvd/internal/library"
	"mvd/internal/torrent"
)

const torrentUsage = `usage: mvd torrent <command>

  add [-movie|-tv] SOURCE...   add downloads: magnet links, links to .torrent files, or
                              .torrent files; they download into the import folder
  list                        what Transmission is doing, and what's ready to import
  pause [ID|NAME...]          pause downloads (or seeding): all of them, or those
                              named (an ID from 'list', or part of a name)
  resume [ID|NAME...]         start paused downloads again: all of them, or those named
  verify [ID|NAME...]         check the data on disk, then start them again: those
                              Transmission reports an error for, or those named
  remove [-keep] [-y] ID|NAME...
                              take those named out of Transmission: finished ones keep
                              their files; unfinished ones lose their partial data,
                              unless -keep
  clear [-keep] [-y]          empty Transmission's list: finished downloads keep their
                              files (they're in the import folder); unfinished ones
                              lose their partial data, unless -keep

Finished downloads are imported by 'mvd import' once they've seeded to
seed_ratio (or straight away if it's 0), and removed from Transmission then.
`

func setupTransmission(kind string) int {
	switch kind {
	case "openwrt":
		fmt.Print(torrent.Setup(config.Load()))
	case "alpine":
		fmt.Print(torrent.SetupAlpine(config.Load()))
	default:
		fmt.Fprintf(os.Stderr, "mvd: no Transmission setup for %q: openwrt or alpine\n", kind)
		return 2
	}
	return 0
}

func cmdTorrent(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(os.Stderr, torrentUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cfg := config.Load()
	switch args[0] {
	case "add", "list", "clear", "resume", "verify", "pause", "remove":
	default:
		fmt.Fprint(os.Stderr, torrentUsage)
		return 2
	}
	if cfg.Transmission == "" {
		fmt.Fprintln(os.Stderr, "mvd: Transmission isn't set up: add this to ~/.config/mvd/config.toml\n\n"+
			"  [downloads]\n  transmission = \"http://127.0.0.1:9091/transmission/rpc\"\n\n"+
			"('mvd setup transmission' prints the router side)")
		return 2
	}
	tr := torrent.New(cfg)
	switch args[0] {
	case "list":
		return torrentList(cfg, tr)
	case "clear":
		return torrentClear(tr, args[1:])
	case "resume":
		return torrentResume(tr, args[1:])
	case "verify":
		return torrentVerify(tr, args[1:])
	case "pause":
		return torrentPause(tr, args[1:])
	case "remove":
		return torrentRemove(tr, args[1:])
	}

	fs := newFlagSet("mvd torrent add")
	movie := fs.Bool("movie", false, "it's a movie: download into import/movies")
	tv := fs.Bool("tv", false, "it's TV: download into import/tv")
	sources, rc := parseFlags(fs, args[1:])
	if rc >= 0 {
		return rc
	}
	if len(sources) == 0 || (*movie && *tv) {
		fmt.Fprintln(os.Stderr, "usage: mvd torrent add [-movie|-tv] SOURCE...")
		return 2
	}
	kind := ""
	if *movie {
		kind = "movie"
	} else if *tv {
		kind = "tv"
	}
	rc = 0
	for _, src := range sources {
		var file []byte
		if !torrent.IsLink(src) {
			b, err := os.ReadFile(src)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mvd: %s isn't a magnet link, a link, or a file mvd can read\n", src)
				rc = 1
				continue
			}
			file = b
		}
		t, dup, warn, err := torrent.Add(cfg, tr, src, file, kind)
		var still *torrent.StillAdding
		if errors.As(err, &still) {
			fmt.Println("…", err)
			t, err = still.Finish()
		}
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "mvd:", err)
			rc = 1
		case dup:
			fmt.Printf("= %s is already in Transmission\n", t.Name)
		default:
			fmt.Printf("+ %s\n", t.Name)
		}
		if warn != "" {
			fmt.Println("  note:", warn)
		}
	}
	return rc
}

func torrentList(cfg config.Config, tr *torrent.Client) int {
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(ts) == 0 {
		fmt.Println("no downloads")
		return 0
	}
	torrent.InQueueOrder(ts)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, t := range ts {
		state, _ := torrent.State(t, cfg.SeedRatio)
		where := ""
		if cfg.ImportDir != "" && !library.Within(cfg.ImportDir, t.DownloadDir) {
			where = "  (outside the import folder: mvd won't import it)"
		}
		fmt.Fprintf(w, "%d\t%s\t%s%s\n\t\t%s\n", t.ID, t.Name, state, where, torrent.Details(t))
	}
	w.Flush()
	return 0
}

func torrentClear(tr *torrent.Client, args []string) int {
	fs := newFlagSet("mvd torrent clear")
	keep := fs.Bool("keep", false, "keep the partial data of unfinished downloads too")
	yes := fs.Bool("y", false, "don't ask")
	if _, rc := parseFlags(fs, args); rc >= 0 {
		return rc
	}
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(ts) == 0 {
		fmt.Println("no downloads")
		return 0
	}
	if !*yes {
		if rc := askDiscard(ts, *keep, "cleared", "clear"); rc >= 0 {
			return rc
		}
	}
	if err := torrent.Discard(tr, ts, *keep); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	fmt.Printf("cleared %d download(s)\n", len(ts))
	return 0
}

// askDiscard asks before removing ts, saying what happens to their data.
// It returns -1 to go ahead, or the exit code if not.
func askDiscard(ts []torrent.Torrent, keep bool, done, verb string) int {
	partial := 0
	for _, t := range ts {
		if t.PercentDone < 1 {
			partial++
		}
	}
	what := fmt.Sprintf("Remove %d download(s) from Transmission", len(ts))
	if len(ts) == 1 {
		what = "Remove " + ts[0].Name + " from Transmission"
	}
	switch {
	case partial > 0 && !keep:
		what += fmt.Sprintf(", deleting the partial data of %d unfinished", partial)
	case partial > 0:
		what += ", keeping the partial data of the unfinished ones"
	}
	if n := len(ts) - partial; n > 0 {
		what += fmt.Sprintf("; %d finished one(s) keep their files", n)
	}
	fmt.Print(what + "? [y/N] ")
	line, err := stdin().ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Println()
		fmt.Fprintf(os.Stderr, "mvd: no answer, so nothing %s (add -y to %s without asking)\n", done, verb)
		return 2
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Println("left as it was")
		return 0
	}
	return -1
}

func torrentRemove(tr *torrent.Client, args []string) int {
	fs := newFlagSet("mvd torrent remove")
	keep := fs.Bool("keep", false, "keep the partial data of unfinished downloads")
	yes := fs.Bool("y", false, "don't ask")
	names, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mvd torrent remove [-keep] [-y] ID|NAME... ('mvd torrent clear' removes them all)")
		return 2
	}
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	picked, err := torrent.Pick(ts, names, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if !*yes {
		if rc := askDiscard(picked, *keep, "removed", "remove"); rc >= 0 {
			return rc
		}
	}
	if err := torrent.Discard(tr, picked, *keep); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	for _, t := range picked {
		fmt.Printf("- %s\n", t.Name)
	}
	return 0
}

func torrentPause(tr *torrent.Client, args []string) int {
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Active)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(picked) == 0 {
		fmt.Println("nothing going")
		return 0
	}
	if err := tr.Stop(picked); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	for _, t := range picked {
		fmt.Printf("|| %s\n", t.Name)
	}
	return 0
}

func torrentResume(tr *torrent.Client, args []string) int {
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Paused)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(picked) == 0 {
		fmt.Println("nothing paused")
		return 0
	}
	if err := tr.Resume(picked); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	for _, t := range picked {
		fmt.Printf("> %s\n", t.Name)
	}
	return 0
}

func torrentVerify(tr *torrent.Client, args []string) int {
	ts, err := tr.Torrents()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	picked, err := torrent.Pick(ts, args, torrent.Torrent.Failed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(picked) == 0 {
		fmt.Println("nothing in error")
		return 0
	}
	if err := tr.Verify(picked); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	for _, t := range picked {
		fmt.Printf("~ %s: checking its files, then it starts again\n", t.Name)
	}
	return 0
}
