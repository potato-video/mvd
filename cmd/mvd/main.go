// mvd is a small media library. It imports movies and TV into a tidy library,
// with Kodi/Jellyfin-style NFOs and artwork from TMDB, and serves it: a JSON
// API, a web UI, and streaming with watch progress.
//
// Optional parts are build tags: webui (the web pages) and torrent
// (Transmission downloads). See the Makefile.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"mvd/internal/config"
	"mvd/internal/tmdb"
)

const usage = `usage: mvd <command> [args]

commands:
  import [flags] [PATH]        bring new movies and episodes into the library: anything
                               in import_dir (or PATH) is identified, matched on TMDB,
                               renamed and moved into movies/ or tv/, and what's left
                               over goes to the trash
  refresh [movies|tv] [flags]  tidy what's already in the library: fetch missing
                               metadata, fix names, -order dvd and the like
  torrent add|list|clear|resume
                               downloads via Transmission, into the import folder
  server [-address A] [-port N]
                               serve the library: a JSON API, streaming, subtitles,
                               artwork, watch progress and the web UI
  progress list|clear|watched  what you're part-way through; forget something you
                               glanced at, or mark it watched (needs mvd server)
  upload [-movie|-tv] PATH...   send files or folders to a server's import folder, from
                               another machine; resumable: run it again to carry on
  search WORDS...              find movies, shows and episodes by title, collection,
                               director or year (needs mvd server)
  status [-json] [-every 5s]   how the server is doing: uptime, memory, library, uploads
  pair [-token NAME] [CODE]    let an Apple TV or phone in: the devices waiting, or
                               approve the one showing CODE (needs mvd server)
  setup config|service|memory|transmission|token
                               print a commented config.toml, the router's Transmission
                               setup, a service to run mvd server at start-up, or a
                               new token
  version                      this build's version and features, and the running server's

'mvd import -h' and 'mvd refresh -h' list their flags.

config: ~/.config/mvd/config.toml; 'mvd setup config' prints one, every setting
explained. TMDB_API_KEY in the environment overrides [tmdb] api_key.
`

// version is set by the Makefile (-ldflags -X main.version=...).
var version = "dev"

// features are the optional parts built in (build tags), added by init funcs
// in features_*.go.
var features []string

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, usage)
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	if w := rootWarning(args[0], os.Geteuid(), serviceKind()); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	switch {
	case args[0] == "import":
		os.Exit(cmdImport(args[1:]))
	case args[0] == "refresh":
		os.Exit(cmdRefresh(args[1:]))
	case args[0] == "torrent":
		os.Exit(cmdTorrent(args[1:]))
	case args[0] == "progress":
		os.Exit(cmdProgress(args[1:]))
	case args[0] == "upload":
		os.Exit(cmdUpload(args[1:]))
	case args[0] == "search":
		os.Exit(cmdSearch(args[1:]))
	case args[0] == "status":
		os.Exit(cmdStatus(args[1:]))
	case args[0] == "pair":
		os.Exit(cmdPair(args[1:]))
	case args[0] == "server":
		os.Exit(cmdServe(args[1:]))
	case args[0] == "setup":
		os.Exit(cmdSetup(args[1:]))
	case args[0] == "version", args[0] == "-version", args[0] == "--version":
		fmt.Println(versionString())
		fmt.Println(serverVersion(config.Load()))
		fmt.Println(tmdb.Notice)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func newFlagSet(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }

// parseFlags allows flags either side of the arguments. It returns the
// arguments, and an exit code >= 0 if the command should stop there.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, int) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return nil, 0
			}
			return nil, 2
		}
		if fs.NArg() == 0 {
			return pos, -1
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func stdin() *bufio.Reader { return bufio.NewReader(os.Stdin) }

func versionString() string {
	f := slices.Sorted(slices.Values(features))
	if len(f) == 0 {
		f = []string{"none"}
	}
	return fmt.Sprintf("mvd %s (%s/%s), features: %s", version, runtime.GOOS, runtime.GOARCH, strings.Join(f, " "))
}
