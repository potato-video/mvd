//go:build files

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"mvd/internal/config"
	"mvd/internal/upload"
)

const uploadUsage = `usage: mvd upload [-server URL] [-token T] [-movie|-tv] PATH...

Sends files or folders to an mvd server's import folder, as the web UI's
Incoming page does: in pieces, picking up after a dropped connection, so a
multi-GB file survives bad Wi-Fi. A folder keeps its layout (only its
videos, subtitles and .nfo files go, and .zip and .rar archives, which
the importer unpacks, in builds with unpack) and arrives whole once all
of it is in.
Stopped part way? Run the same command again: it carries on where it got
to, and skips what's in.

  -server URL   the server; default: [client] server in config.toml, else this
                machine's, else the one found on the local network
  -token T      if the server wants one; default: [server] token in config.toml
  -movie, -tv    into import/movies or import/tv, for when the name doesn't say
`

func cmdUpload(args []string) int {
	cfg := config.Load()
	fs := newFlagSet("mvd upload")
	fs.Usage = func() { fmt.Fprint(os.Stderr, uploadUsage) }
	server := fs.String("server", "", "")
	token := fs.String("token", cfg.Token, "")
	movie := fs.Bool("movie", false, "")
	tv := fs.Bool("tv", false, "")
	paths, rc := parseFlags(fs, args)
	if rc >= 0 {
		return rc
	}
	if len(paths) == 0 || (*movie && *tv) {
		fmt.Fprint(os.Stderr, uploadUsage)
		return 2
	}
	kind := ""
	if *movie {
		kind = "movie"
	} else if *tv {
		kind = "tv"
	}
	if *server == "" {
		found, ok := findServer(cfg, os.Stderr)
		if !ok {
			return 1
		}
		*server = found
	}
	if !strings.Contains(*server, "://") {
		*server = "http://" + *server
	}
	items, err := upload.Plan(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}

	tty := false
	if st, err := os.Stdout.Stat(); err == nil {
		tty = st.Mode()&os.ModeCharDevice != 0
	}
	var t0 time.Time
	var from int64
	lastLine := time.Time{}
	c := &upload.Client{Base: *server, Token: *token}
	c.Progress = func(p upload.Progress) {
		switch {
		case p.Already:
			fmt.Printf("= %s (in already)\n", p.Name)
		case p.Done:
			if tty {
				fmt.Print("\r\033[K")
			}
			fmt.Printf("✓ %s, %s\n", p.Name, sizeStr(p.Size))
			if p.FolderDone {
				fmt.Printf("  the folder is all in: it's in the import folder\n")
			}
		case p.Note != "":
			if tty {
				fmt.Print("\r\033[K")
			}
			fmt.Printf("  %s: %s\n", p.Name, p.Note)
		default:
			// Now and then: every update on a terminal, every 30s otherwise.
			if !tty && time.Since(lastLine) < 30*time.Second {
				return
			}
			lastLine = time.Now()
			line := fmt.Sprintf("↑ %s  %d%%, %s of %s", p.Name, p.Sent*100/max(p.Size, 1), sizeStr(p.Sent), sizeStr(p.Size))
			if secs := time.Since(t0).Seconds(); secs > 2 && p.Sent > from {
				rate := float64(p.Sent-from) / secs
				line += fmt.Sprintf(", %s/s, %s left", sizeStr(int64(rate)),
					short(time.Duration(float64(p.Size-p.Sent)/rate)*time.Second))
			}
			if tty {
				fmt.Print("\r\033[K" + line)
			} else {
				fmt.Println(line)
			}
		}
	}
	rc = 0
	for _, it := range items {
		t0, from = time.Now(), 0
		if err := c.Send(it, kind); err != nil {
			if tty {
				fmt.Print("\r\033[K")
			}
			fmt.Fprintln(os.Stderr, "mvd:", err)
			rc = 1
			if err == upload.ErrToken {
				break
			}
		}
	}
	return rc
}
