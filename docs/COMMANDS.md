# Commands

Every `mvd` command, and how it behaves. For installing mvd, see the
[README](../README.md); for its settings, [CONFIG.md](CONFIG.md); for
`mvd torrent`, [DOWNLOADS.md](DOWNLOADS.md).

```
mvd import [-n] [-y] [PATH]        bring new things into the library
mvd import -copy PATH              the same, copying from elsewhere
mvd refresh [movies|tv] [flags]    tidy what's already there
mvd server [-address A] [-port N]  serve it
mvd status [-json] [-every 5s]     how the server is doing
mvd search [-json] WORDS...        find movies, shows and episodes
mvd progress [-as NAME] list|show|clear|watched
mvd upload [-movie|-tv] PATH...    send files to a server, from another machine
mvd pair [-token NAME] [CODE]      let an Apple TV in
mvd torrent ...                    downloads (see DOWNLOADS.md)
mvd setup config|service|memory|transmission|token
mvd version
```

`mvd <command> -h` lists a command's flags.

## `mvd import`

Each file or folder in `import/` is one thing to import: a movie, an
episode, a season pack, a whole series in season folders, or a mix. mvd
works out which is which (`S01E02`, `1x02` and season folders mean TV;
anything else is taken for a movie), matches it on TMDB, and files it into
`movies/` or `tv/` with its subtitles (including `Subs/` folders). New
episodes join their show's existing folder.

- `-n` shows what would happen and changes nothing. `-y` never asks: it
  files what it's sure of and leaves the rest.
- **Unsure matches** are asked about at the terminal, each item's questions
  before any of it is filed. `q` stops the whole import: that item and
  everything after it stay in `import/` (what's filed already stays filed).
  With `-y`, unsure matches stay in `import/`. To settle one, put a `.mvd` file in its folder (or `Name.mvd`
  beside a single file):

  ```toml
  kind  = "tv"      # or "movie"
  tmdb  = 95396     # the TMDB id, from the address of its TMDB page
  order = "dvd"     # TV only: the episode order to use

  # TV only: which episode a video is, when its name doesn't say, or says
  # wrong (its path in the folder, and the episode)
  [episodes]
  "S02/Ghosts S02E07 The Ghost of Christmas.mkv" = "S00E01"
  ```

- **An episode TMDB doesn't have** by the number in its name (a Christmas
  special released as "S02E07", say) is looked for among the show's
  specials by the title in its name. It's filed as that special only when
  one, and only one, matches closely. Otherwise it's asked about (which
  episode it is: the closest by title, or one typed as `S00E01`), and with
  `-y` it stays in `import/` for an `[episodes]` line in the hint.

- **Folders named `movies/` and `tv/`** inside `import/` force the type of
  what's in them, for things with no episode numbers in their names.
- **Files still arriving** are left until they've been unchanged for a
  minute. Half-finished downloads (`.part` and the like) are left until
  they're done.
- **Leftovers.** Once everything in an item has been filed, the rest
  (samples, release `.nfo` and `.txt` files) goes to `.trash/`, which is
  emptied by age and size (`trash_max_age`, `trash_max_size`). An item with
  anything mvd couldn't file is left as it is. The trash only ever deletes
  what mvd put in it.
- **Duplicates.** Something already in the library is left in `import/`
  rather than replacing it.
- `-copy PATH` imports from somewhere else (a USB stick, another share) by
  copying, and leaves the source untouched.
- Every move is recorded in `import.log` under `data_dir`.
- One import runs at a time, whether from the command line or the server.
  After an import, a server running on the same machine rescans by itself.
  If it doesn't answer, mvd tries again a few times over about half a minute.
- **Archives** (`.zip`, `.rar`, and split sets: `.part01.rar` or `.rar`
  with `.r00`, `.r01`, ...) are unpacked, with the `unpack` feature (in the
  standard build), beside themselves, then the item is
  imported, and the archives go to the trash with the other leftovers. A
  loose archive in `import/` is first moved into a folder named after it,
  with the rest of its set and its `Name.mvd` hint. Only videos, subtitles
  and `.nfo` files come out, flattened to their own names (no folders), and
  each is checked against the size it declares (and a RAR's against its
  CRC). See [the FAQ](FAQ.md#an-archive-wasnt-unpacked) for what can't be
  unpacked: compressed RARs without `unrar`, `.7z`, passwords.
  - Unpacking needs room for what comes out, since the archives stay until
    the item is filed (and in the trash after that, when it's on the same
    drive). Short of room, mvd clears the trash's oldest leftovers if that's
    enough, and otherwise says how much it needs and leaves the item.
  - A download still seeding whose videos are all in archives is unpacked
    and filed straight away; the archives stay, seeding, and go to the trash
    once it's done. See [DOWNLOADS.md](DOWNLOADS.md).
  - `-n` lists what it would unpack. `-copy` doesn't unpack (it never writes
    to the source).

The server imports by itself (`auto_import`, on unless set to `false` under
`[library]`): anything new in `import/` (a finished upload or download, or
files copied in) is filed within a minute or so. A finished upload goes at
once; anything else once it's been unchanged for a minute. What mvd isn't
sure of stays there for you, and isn't tried again until the folder changes.

## `mvd refresh`

```
mvd refresh [movies|tv] [-n] [-y] [-all] [-force] [-no-rename]
            [-movie NAME | -show NAME [-season N]]
```

Fetches metadata and artwork for anything in the library that's missing it,
and fixes names. `-all` fetches everything again. `-force` matches again
from scratch.

`-movie NAME` limits it to movies whose name contains NAME, and `-show NAME`
to shows; either one picks the library, so `movies` or `tv` isn't needed.
`-season N` (with `-show`) limits it further to one season, `0` for
specials, which saves a lot of work on a long show:

```bash
mvd refresh -show "doctor who" -season 4 -all
```

For a show whose files follow DVD or production order rather than broadcast
order:

```bash
mvd refresh tv -show batman -order dvd
```

This files the show's episodes again under TMDB's DVD order (asking which,
if there are several) and remembers the choice in the show folder's `.mvd`
file. The orders are `aired`, `dvd`, `production`, `digital`, `absolute`,
`tv`, `pick` (choose from all of them), or a TMDB episode group id. It
re-files the whole show, so it can't go with `-season`.

## `mvd server`

Serves the API, the streams and (if built in) the web UI on one port, 6837
by default. Watch progress is kept in `state.json` under `data_dir`. Files
are sent as they are, with HTTP range support, so players can seek.

## `mvd status`

```
mvd status              # build, uptime, memory, CPU, library, uploads
mvd status -every 5s    # a line every 5 seconds, to watch an upload or a rescan
mvd status -json        # GET /api/status, as it is
```

It asks the server on this machine, at `[server] address` and `port`
(`127.0.0.1` if the server listens on every interface), or the one
`[client] server` names.

On a machine with no server of its own and no `[client] server`, `status`,
`search`, `progress` and `upload` look for one on the local network (by
Bonjour, which takes a second) and use it, saying which:

```
mvd: using GL-AR750S at http://192.168.8.1:6837, found on the network
```

If several answer, they're listed and nothing is done: pick one with
`-server` (`search` and `upload`) or `[client] server`. The token still
comes from `[server] token` or `-token`.

```
mvd 99bb558 on linux/mips (go1.24.7), up 3h 13m
memory   15.6 MB in use, 17.2 MB at most; Go has 9.1 MB, 4.2 MB of it live; 12 GCs
library  312 movies, 41 shows, 1890 episodes; 1.2 TB, 3.4 TB free
busy     13 goroutines, 1 upload under way
cpu      3.2% now; 4m 12s since it started; load 0.42 0.31 0.25
```

"In use" is what `top` and `free` count, and "at most" is the peak since it
started. The library's size is its videos', and "free" what's left on its
drive. "Go has" is what the Go runtime has taken from the system, and
"live" what it's using of that now. CPU "now" is its share of one core over
the last 10 seconds. The load averages are the whole system's, over 1, 5 and
15 minutes (under 1 on a single core is comfortable).

Hovering over the mvd logo in the web UI shows the build, memory, CPU and
uptime (click to copy the build).

## `mvd search`

```
mvd search god fa           # The Godfather, and anything else with those words
mvd search coppola 1974     # by director and year
mvd search -json sopranos   # GET /api/search, as it is
```

Searches the library as the website and the Apple TV app do: the server
does the searching, so they all find the same things.

- **What's searched:** titles (a movie's, a show's, an episode's), a
  movie's collection, its directors, and the year. Not genres, plots or
  cast.
- **Every word must start a word,** in any order, and a few letters of
  each will do: `god fa` finds "The Godfather Part II" (a word can carry on
  where the one before stopped), but `father` doesn't.
- **Case, accents and punctuation don't count:** `amelie` finds "Amélie",
  `oceans eleven` "Ocean's Eleven".
- **Best first, at most 40 of each:** the whole title, then titles starting
  with what's typed, then titles with every word, then matches on a
  collection, director or year. A show's name lists the show, then its
  episodes.

```
movie    The Godfather (1972)          m238
movie    The Godfather Part II (1974)  m240
show     Severance (2022)              s95396
episode  Severance S01E02 Half Loop    e95396102
```

Each line ends with the id, which `mvd progress watched` and `clear` take.
It exits 1 when nothing's found. Like `mvd status`, it asks the server on
this machine, or the one `[client] server` names; `-server URL` and
`-token T` name another.

## `mvd progress`

```
mvd progress list                       # Up Next, with ids and how far in
mvd progress show "Severance"           # every episode's state, and why it is (or isn't) in Up Next
mvd progress clear "Star City S01E01"   # back to never started
mvd progress watched "Severance"        # mark every episode watched
mvd progress watched -show sopranos -season 1   # just the first season
mvd progress -as kids list              # another token's Up Next
```

A name is part of a movie's or show's title, plus an episode code for a
single episode; an id works too. In place of names, `clear` and `watched`
take `-movie NAME` or `-show NAME` to look only among movies or shows, as
`mvd refresh` does, and `-show NAME -season N` for one season (`0` for
specials). The server keeps the watch history, so it
must be running. Less than `resume_after` (a minute, by default) into
something doesn't count as starting it, so a quick look doesn't put it in
Up Next.

With several [tokens](CONFIG.md#tokens), each has its own watch
history. `mvd progress` works on the admin's; `-as NAME`, before the
command, works on another's, seeing only what that token can see.

## `mvd upload`

From another machine with mvd on it (built with the `files` feature, as the
standard build is):

```bash
mvd upload -server router:6837 -tv "Severance S01" "Dune (2021).mkv"
```

It sends files and folders in pieces, carrying on after dropped connections.
Stopped part way, run it again and it picks up where it got to and skips
what's already there. Without `-server` it uses the server it finds on the
local network, if there's just the one (see `mvd status`). Set
`server = "http://router:6837"` under `[client]` in that machine's config
to name it, and `[server] token` to save `-token`. `-movie` and `-tv` send to `import/movies` or `import/tv`.

## `mvd pair`

```
mvd pair                        # the devices waiting to be let in
mvd pair CC6W-TAB6              # let that one in, as admin
mvd pair -token kids CC6W-TAB6
```

An Apple TV signing in shows a code under its square one. `mvd pair` with
that code gives the TV access as the token named (the admin's, with
no `-token`), without a password being typed on it. Check the code
against the device's screen first. Codes last five minutes. The
website's Pair page does the same from a browser. It asks the server on
this machine, with `[server] token`, so the server must be running.

## `mvd setup`

`mvd setup` prints things for you to read and save. It never changes
anything itself.

| | |
|---|---|
| `config` | a commented config with every setting |
| `service [procd\|openrc\|systemd\|launchd]` | a service file for this machine, with this binary's path filled in |
| `memory` | a kernel setting for small machines without swap (see [SETUP_OPENWRT.md](SETUP_OPENWRT.md#memory)) |
| `transmission [openwrt\|alpine]` | the commands to set up Transmission for mvd, for this machine (see [DOWNLOADS.md](DOWNLOADS.md)) |
| `token [NAME]` | a new random token |

`mvd setup token` prints a new random token to paste into config.toml
as `[server] token`. `mvd setup token kids` prints a whole
`[[server.tokens]]` entry with that name. Neither changes the config.

## `mvd version`

Shows this binary's build and features, and the running server's, so you can
tell whether a restart is needed. Then the notice TMDB's terms ask for: "This
product uses the TMDB API but is not endorsed or certified by TMDB."

