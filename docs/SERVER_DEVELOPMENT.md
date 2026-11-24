# Server development

Working on the Go server and CLI, the web UI, and the HTTP API. For using
mvd, see the [README](../README.md).

## Principles

**It runs on a potato.** The reference machine is a GL-AR750S router:
32-bit big-endian MIPS at 775 MHz, 128 MB of RAM shared with Transmission
and the router itself, no swap, and the library on an exFAT USB drive. Every
change is weighed against that:

- **Standard library only, no cgo.** One static binary that cross-compiles
  from anywhere with `GOOS`/`GOARCH`. No modules in `go.mod`.
- **Watch the binary size.** `make sizes` before and after anything sizable.
  Optional features are build tags, so a minimal build stays minimal.
- **Watch memory.** Stream to and from the drive rather than buffering
  whole files. Never let Go spill to `/tmp` (it's RAM on OpenWRT): no
  multipart forms for large bodies. Mind garbage on hot paths (polling,
  scanning), since every collection costs CPU the router doesn't have.
- **Mind exFAT.** No file ownership, no hard links, no sparse files (setting
  a file's size means writing zeros), and timestamps that copies may set
  back. Renames on the same drive are cheap; anything else over USB 2 is
  slow.
- **No transcoding on the router.** Files are served as they are. Anything
  that needs ffmpeg belongs behind its own build tag, for machines that can
  run it.
- **Simple over complete.** mvd isn't Plex or Jellyfin. Leave out what most
  people won't use, rather than adding a setting.

## Layout

```
cmd/mvd/            the command line: one file per command (import.go, torrent.go, ...)
internal/config/    config.toml: one table of settings, by "section.key"
internal/library/   names, matching, NFOs, import and refresh, unpacking, the library index, search, ffprobe
internal/lang/      language codes and their English names (ISO 639-1 and 639-2, BCP 47, TMDB's)
internal/archive/   reading .zip and .rar (stored RAR4 and RAR5, across volumes)
internal/tmdb/      the TMDB client (movies, TV, episode groups), with its cache
internal/torrent/   the Transmission client, and the OpenWRT setup script
internal/upload/    the upload protocol, client side (mvd upload)
internal/mdns/      a small Bonjour responder
internal/server/    the API, streaming, subtitles, uploads, Up Next, the web UI
  ui/templates/     html/template pages and partials
  ui/static/        CSS, htmx, home.js, incoming.js, the Bricolage font, icons
internal/testutil/  fake TMDB and Transmission servers, file helpers, for tests
api/openapi.yaml    the HTTP API, for writing clients
deploy/             config.example.toml, and service files (openwrt/, openrc/, systemd/,
                    launchd/),
                    built into the binary for mvd setup
scripts/            test-server.sh: a throwaway server with a made-up library;
                    reset-library.sh: a library and its server put in a
                    known state; install.sh: the installer
```

## Building and testing

```bash
make                   # mvd for this machine, standard features
make router            # big-endian MIPS, for the GL-AR750S
make PRESET=minimal    # no optional features
make FEATURES="webui"  # any mix
make test              # go vet and go test for every combination of features
make sizes             # router binary size for each combination
make release           # what a release holds, in dist/
```

- **Run `make test` before committing.** It builds and tests every
  combination of tags, so a stub that has drifted from the real code shows
  up.
- **`go test -race`** for anything concurrent (the server, uploads,
  auto-import, mDNS).
- **Tests run against fakes:** `testutil` has a fake TMDB and a fake
  Transmission (both RPC protocols). No test touches the network.
- **`testutil` stays standard library only**, since every package's tests
  import it.
- **Fix bugs test-first:** reproduce the bug in a failing test, then fix it.
- **Counting garbage.** Some tests (`TestTorrentsGarbage`,
  `TestIndexLongSeason`) check allocations on hot paths. They skip under
  `-race` (`testutil.Race`), which allocates as it goes.
- **`scripts/test-server.sh`** builds a made-up library in `/tmp/mvd-test`
  (fifty movies and twenty shows with generated artwork, some part-watched)
  and runs a server on `:6838` with token `test`. Useful for the website and
  the Apple app. It needs ffmpeg for the artwork. When each title arrived
  and what's watched it writes as a plan (`reset.txt` in the folder) for
  `reset-library.sh`, which it runs once the server's up, and which puts
  it back as it started at any time.
- **`scripts/reset-library.sh PLAN LIBRARY`** puts a library and the
  server running on it in a known state, as a plan file says: each
  title's added date (its videos' modification times, set relative to
  now, so "new" stays new) and the watch history. It forgets all of the
  token's history over the API, then plays the plan's back in order, so
  the last line is the most recently watched. It's for the demo server
  and tests (`mvd-demogen test` is its default server); the usage and
  the plan's format are at the top of the script. It runs where the
  library is, as the dates are on the files, and needs the admin token
  for the rescan; `-a NAME` resets a viewer's history.

## Releases

Pushing a version tag makes a release: `git tag v1.0 && git push origin v1.0`.
`.github/workflows/release.yml` runs `make test`, then `make release`, and
publishes `dist/` as the tag's GitHub release.

- **What's in it:** `mvd-linux-ARCH` for `mips` and `mipsle` (software
  floating point), `arm` (ARMv6 and up, so every Pi), `arm64` and `amd64`,
  with `SHA256SUMS` and the installer. The builds are `webui files`, without
  `downloads` or `unpack`: see the decision log.
- **The installer** is `scripts/install.sh`, plain `sh` (BusyBox's and
  dash), which potato.video serves too. It picks the build from `uname -m`
  (and `/bin/sh`'s ELF header, for which MIPS), checks it against
  `SHA256SUMS`, and does what the setup guides do by hand: the binary on the
  drive beside the library on OpenWRT and OpenRC, the config from
  `mvd setup config` with the answers put in, the service from
  `mvd setup service` (and lingering turned on, for systemd's user
  service). With a config already there it only replaces the
  binary and restarts. Its questions can be answered ahead in the
  environment (`MVD_VIDEO_DIR`, `MVD_TMDB_KEY`, `MVD_YES`), and
  `MVD_RELEASE_URL` points it at other builds: `make release`, serve
  `dist/` and run it against that to try a change. Keep it in step with the
  guides.
- **The site:** potato.video is a repo of its own, `mvd-site`: a static
  page in the apps' look, which App Review may read, so it says nothing of
  downloads. It keeps copies of `docs/PRIVACY_POLICY.md`, the web UI's
  typeface and `scripts/install.sh`, made by its `sync.sh`: after changing
  one of them here, sync and commit there.

## Code style

- `gofmt`, `go vet`, and the standard library's conventions.
- **American English** in code, comments, commit messages and docs: color,
  behavior, canceled. (User-facing text in the Apple app is localized, with
  a British English catalog, and that stays.)
- **Plain, direct comments** that say what something is for and why,
  especially where the reason is the router (memory, exFAT, the jail).
- **Files mvd keeps are written whole** with `library.WriteFileAtomic`: a
  temporary file, synced, renamed over the old, and the folder synced. The
  router loses power; without the syncs a rename can land on an empty
  file. A copy across drives (`copyFile`) is synced before its source goes.
- **Commands print, they don't configure.** `mvd setup` prints scripts and
  files for people to read and run. mvd never edits system config itself.
- **Commit messages** say what changed and why, in a sentence or two.
- **Docs change with the code.** A change to behavior updates the user docs
  (README, CONFIG, COMMANDS, DOWNLOADS, FAQ or CLIENTS); a change to the API updates `api/openapi.yaml`; a design change
  updates this file; a decision worth remembering goes in the decision log.

## Build tags

A feature is a build tag. Its code is in files with `//go:build TAG`, and a
`!TAG` file holds no-op stubs with the same names (`downloads_none.go`,
`webui_none.go`, `notorrent.go`, `unpack_none.go`, `uploads_none.go`). A package used only by
the feature (`internal/torrent`, `internal/archive`) is only linked when a tagged file imports it.
`cmd/mvd/features_*.go` adds the feature's name for `mvd version`.

| Tag | What |
|---|---|
| `webui` | the web pages (html/template, htmx) |
| `files` | uploads (`uploads.go`, `uploads_folder.go`, `internal/upload`, `mvd upload`), the Needs you API (`needs.go`), and with `webui`, the Incoming page |
| `torrent` | `mvd torrent`, `/api/downloads`, the import's seeding check |
| `unpack` | unpacking archives on import (`internal/archive`), and taking them as uploads |

- **What every build keeps:** `mvd import` and its terminal questions,
  auto-import, `POST /api/import`, and the importer's list of finished
  uploads (`uploads_core.go`), empty without `files`. A library filled by
  hand and filed with `mvd import` needs nothing more.
- **Web files go by both tags:** `webui && files` for Incoming,
  `webui && downloads && files` for its downloads, with stubs for each
  combination left over (`webui_noincoming.go`, `webui_downloads_nofiles.go`).
  Incoming's template is only parsed when it's built (`pageTemplates`).
- **The tags are independent.** `downloads` or `unpack` without `files` builds
  and works (from the command line and the API), but `make` warns, since
  they lose their part of the website and archive uploads.

The presets are in the Makefile: `minimal` (none), `standard`
(`webui files downloads unpack`) and `full` (everything in `ALL`). A future `ffmpeg` tag
would go in `ALL` and `full`, not `standard`.

### Binary size

On the router (`GOARCH=mips`, `-ldflags='-s -w'`):

| Features | Size |
|---|---|
| none | 8.13 MB |
| webui | 10.50 MB |
| files | 8.31 MB |
| downloads | 8.31 MB |
| unpack | 8.31 MB |
| webui files | 10.69 MB |
| webui files downloads | 10.88 MB |
| webui files downloads unpack | 11.13 MB |

- **About 6.5 MB is the floor:** the Go runtime, `net/http`, and the TLS
  needed to talk to TMDB.
- **The web UI is 2.4 MB**, but its files (htmx, the font, CSS, templates)
  are only about 0.25 MB. The rest is `html/template`: executing a template
  calls methods by name through reflection, and once that's reachable, the
  linker can't remove unused methods anywhere in the binary. Replacing it is
  planned.
- **Sizes move in 64 KB steps,** since the MIPS linker pads each section. A
  few KB of new code can show as nothing, or as a whole step.
- **No UPX.** See the decision log.

## Configuration

`internal/config` has one table of settings, `"section.key"` to a setter.
`config.Settings()` lists them. A setting outside its section, or unknown,
is ignored with a warning naming the right section.

- `deploy/config.example.toml` is the short documentation, printed by
  `mvd setup config`. A test in `deploy` checks it has exactly the settings
  in the table, in their sections. [CONFIG.md](CONFIG.md) is the long one.
  Add a setting to all three.
- `deploy/embed.go` builds the example and the service files into the binary
  for `mvd setup`. It's a package at the module root because `go:embed`
  can't reach outside its package's folder.
- `[client]` holds settings for the client commands, not the server.
- **`[[server.tokens]]`** is the one array of tables. Each `[[...]]` line
  starts an entry, and the keys after it (`tokenSettings`: `name`, `token`,
  `ratings`, `unrated`) are that entry's until the next `[section]`.
  `ratings` is a one-line array of quoted strings (`parseList`), and
  nothing more: this isn't a TOML parser. `Settings()` lists the keys as
  `server.tokens.name` and so on, for the example's test. The checks that
  stop the server starting (`CheckTokens`) aren't in `Load`, so the other
  commands run whatever the tokens say.

## Import

The importer is in `internal/library`: `import.go` (the `Importer`),
`plan.go` (reading an item), `parse.go` and `parse_tv.go` for names,
`match.go` for TMDB, `nfo*.go` for writing.

The NFOs carry TMDB's `original_language` as Kodi's `<originallanguage>`
(Kodi v22), in BCP 47: TMDB's `xx` (no language) is written `zxx`, its `cn`
(Cantonese) `yue`. Other tools write it as ISO 639-1 or 639-2, BCP 47
(`fr-CA`) or an English name, so the index reads it through `lang.Code`,
which turns all of those into one code ("fr"). A library filed before the
tag was added gets it from `mvd refresh -all`.

- **Three types.** `filer` (`filer.go`) is what import and refresh share:
  moving files into place (`mv`, sidecars), NFOs and artwork (`apply` for a
  movie, `applyShow` in `tv.go`), matching (`match.go`) and episode orders
  (`order.go`). `Importer` and `Refresher` embed it. Nothing in the filer is
  a mode: the folder to file into, movie or TV, and the episode order are
  arguments. The importer's own state (its source, what moved, the log)
  is in an `importing` the filer sees only during an import.
- **Each item goes through two calls.** `Identify` reads it (waiting for one
  still arriving or seeding, unpacking archives), works out what's in it
  (`planItem`), and matches each movie or show, asking nothing. `File` files
  what was matched and sees to the leftovers. `Run` does both for each item,
  and in between asks about what `Identify` wasn't sure of, if it has an
  `AskFunc`.
- **Questions** go through an `AskFunc` (`ask.go`): `AskTerminal` at a
  terminal (the beets-style prompts), none with `-y` and in the server, which
  leaves the item with its candidates. The library never reads stdin
  itself. A show's episode order, when there are several to choose from, is
  asked during `File`, through the same `AskFunc`.
- **Outcomes are data** (`results.go`): an `ItemResult` per item (filed,
  waiting, seeding, left with a `Problem`, removed) with a `UnitResult` per
  movie or show (filed, a choice to make with up to five candidates,
  skipped, duplicate, failed). `printItem` writes the `…`, `?`, `-`, `=` and
  `!` lines from it, and `Stats` counts it up. The running commentary (the
  moves) is text, written as it happens.
- **Transmission** is asked once per import, by `LoadDownloads` (or, in the
  server, `TransmissionDownloads` through the server's own client), and the
  answer handed to the importer (`SetDownloads`).
- **`CheckImport`** is what has to be true before an import, for `mvd
  import` and the server alike.
- **For the website's Needs you:** `PlanItem` (what an item would file, without
  TMDB), `Search` and `Lookup` (by name, or by a TMDB or IMDb id or
  address), and `WriteHint`, which settles an item for the next import.

- **The unit of import** is each top-level entry in `import/`: a file, a
  movie folder, a season pack, a series, or a mix. Within it, videos are
  grouped: episodes by show, and everything else as movies, with
  `part1`/`part2` kept together.
- **Movie or TV:** episode markers (`S01E02`, `1x02`, "Season 2 Episode 5"),
  or a bare number inside a season folder, mean TV. Anything else is a
  movie. In `import/tv/` or with a hint file, "Part 2" and "05 Title" are
  also read as episode numbers.
- **The show's name** comes from the file's name when that starts with the
  show ("Severance.S01E01..."), otherwise from the nearest folder that isn't
  a season folder. If they differ, both are searched.
- **A movie's name** comes from its folder when the folder is its own (one
  movie in it, with a year or a similar title), otherwise from the file.
- **Folder and file names** are the title through `SafeName`: no characters
  exFAT, FAT or SMB refuse, and none of the leading `.`, `_` or `@` that
  every scan skips (`skipName`), so ".45 (2006)" is filed as "45 (2006)".
- **Existing library folders** are found by TMDB id (from their NFOs) at the
  start of a run. So a duplicate is caught whatever it's called, and new
  episodes join their show wherever it is.
- **Still arriving:** a partial-download name (`.part`, `.crdownload`,
  `.!qB`, `.tmp`), or a change (the later of mtime and ctime) in the last
  minute. The ctime catches a Finder copy that set the mtime back when it
  finished. A folder with no files in it yet, made in the last minute, is
  arriving too: a copy over SMB makes the folder first.
- **Subtitles** travel with their video, including `Subs/` folders (matched
  by folder name, name prefix, or being the only video). They're named
  `.en.srt`, `.en.sdh.srt`, `.en.forced.srt`, and `.2` for further ones.
- **Extras:** folders such as Featurettes and Behind the Scenes go into the
  movie's folder under that name, lowercased, which Jellyfin and Kodi read.
  Files ending `-trailer`, `-featurette` and the like go to `extras/`.
  Samples (by name, under 400 MB, or in `Sample/`) are junk.
- **Leftovers** go to the trash once everything in the item is filed. An item
  with any video left unfiled is left alone, so the trash only holds junk.
- **The trash is guarded:** entries are named `2026-10-27 235218 Original
  name`, and the purge only deletes entries named that way (their age comes
  from the stamp, since a rename keeps the mtime). `checkTrashDir` refuses a
  trash folder that contains, or sits inside, the library or the import
  folder. Trashing only falls back to deleting across drives (`EXDEV`).
- **Filed files get their mtime set to now,** so "new" means new to the
  library, not new on the disc.
- **One import at a time:** `import.lock` in `data_dir` holds the importer's
  pid and, on Linux, the boot id. It's taken over when it's from an earlier
  boot, when that process has gone or is now another program (`comm`), when
  it's this process's pid but this process didn't take it, or after six
  hours. A router's pids come round much the same after every boot.
- **The audit trail:** every move is appended to `import.log` in `data_dir`.
- **`-copy`** never trashes anything, since the source isn't ours. Nor does
  it unpack, which would write into the source.
- **Archives** are unpacked before the item is read; see below.
- **Hint files:** `.mvd` (in a folder) or `Name.mvd` (beside a file) settle
  `kind`, `tmdb` and `order`. A show's own `.mvd` remembers its episode order.

## Refresh

`mvd refresh` is the `Refresher` (`refresh.go`), over the library itself:
`Run` for movies, `RunTV` (in `tv.go`) for shows, with the same filer as
import.

- **Which library:** `cmd/mvd/refresh.go` (`refreshKinds`) goes by `movies`
  or `tv` if given, otherwise by the flags: `-movie` means movies, and
  `-show`, `-season` and `-order` mean TV. Mixing them is a usage error.
- **`-movie` and `-show`** both set `Options.Name`, a loose substring match
  on the folder name or parsed title (`wanted`). Matching nothing is an
  error that points at the other flag.
- **`-season`** (`Options.Season`) filters in `applyShow`, after a file with
  no season number has been given the show's only regular season, and by the
  numbering on disk (the show's remembered order, if it has one). The show's
  folder, `tvshow.nfo`, poster and fanart are still seen to; the episodes,
  their thumbnails and season posters are limited to that season. A show
  with a remembered order still loads every season its episode group
  covers, so there `-season` saves the file work but not the TMDB calls.

### Archives

`internal/archive` reads them; `internal/library/unpack.go` decides what
comes out and where. Both are the `unpack` feature: without it,
`unpack_none.go` says no archive is one mvd can unpack, so they're left in
`import/` and can't be uploaded. (`unpacked.go`, the list of what was
unpacked, and the seeding check that reads it, are always built.)

- **Which:** `.zip`, and RAR sets by their first volume (`.rar`, or
  `.part1.rar`/`.part01.rar`); `.r00` and `.part2.rar` are read with their
  first. `.7z` and split `.001` files aren't unpacked, and an item of only
  those says so.
- **Zip** is `archive/zip`, which checks each entry's CRC and refuses to
  read past its declared size.
- **RAR** is our own reader, for stored data only, in RAR4 and RAR5. It
  walks the block headers (checking their CRCs), follows a file split
  across volumes (the next volume by name: `.part02.rar`, or `.r00`, `.r01`
  … `.r99`, `.s00`), and checks each part's CRC before the last and the
  whole file's at the end, as unrar does. A size that's negative (RAR4's
  64-bit sizes come in two halves) or over 2^62 is damage: the walk only
  ever moves forward. Encrypted headers or entries,
  links, and folders are recognized and never extracted. Data is read with
  `ReadAt` straight into the caller's buffer, so the only memory is one
  header slice. A BLAKE2 hash (RAR5's option instead of CRC32) isn't
  checked. Format: rarlab's technote for RAR5, unrar's source for RAR4
  (including its Unicode file names).
- **Compressed RAR data** goes through `unrar p -inul -p- -y -- ARCHIVE
  ENTRY` when `unrar` is on the PATH (looked up once), one process per
  entry, its output streamed through the same caps. Without it, the item is
  left with a message saying so. unrar allocates a RAR5 entry's whole
  dictionary, so one over a quarter of the RAM (at least 32 MB: on the
  router, 32 MB) isn't unpacked; and unrar is stopped if it sends nothing
  for ten minutes, so a stuck one can't hold the import lock.
- **What comes out:** videos and `UploadExts` (subtitles, `.nfo`) only,
  under their base names (`flatName`: no folders, `..`, absolute paths,
  control characters or hidden names), beside the archive. A name taken
  gets " (2)". Subtitles and `.nfo` files over 64 MB are passed over.
- **Caps:** a zip whose file list (central directory) is over 1 MB isn't
  opened, since `archive/zip` reads it all into memory; an archive with over
  1,000 files to unpack, or declaring over a petabyte, is refused, and so
  is a compressed video that says it's over 20 times its compressed size:
  video doesn't compress, so it's a bomb. Each
  entry is written through `io.LimitReader` at its declared
  size plus one byte, and refused if it has more or less; the total is
  capped at the sum of the declared sizes. Each is written as
  `.mvdtmp` and renamed once whole, and a failed archive removes what it
  wrote (not a file it found there already and kept). One 256 KB buffer for everything, with the file's `ReadFrom`
  hidden so `io.CopyBuffer` uses it.
- **Room:** the sum of declared sizes plus 64 MB must be free. The archives
  stay until the item is filed and, with a trash on the same drive
  (`st_dev`), after that too, so if clearing the trash's oldest entries
  would make room, they're cleared (`purgeTrash` with a lower size limit).
- **`.mvd-unpacked`** in the item lists each archive unpacked and the files
  that came out, so a rerun doesn't unpack again, and an entry already
  there at its declared size (from a run cut short) is kept.
- **Loose archives** in `import/` move into a folder named after them
  (with the rest of the set and `Name.mvd` as `.mvd`), which is then
  imported. A later volume met on its own is passed over when its first
  volume is there.
- **Seeding downloads** whose videos all came out of archives (or are
  still in them) are imported while seeding: the archives and the torrent
  stay, and `finishItem` sends them off once it's ready. One with loose
  videos waits, as before.

## Downloads (Transmission)

`internal/torrent` is the client. `internal/library/downloads_torrent.go`
is the import's side.

- **Both RPC protocols.** Transmission 4.1 moved to JSON-RPC 2.0 with
  snake_case names. The old protocol (camelCase and kebab-case) still works
  in 4.x but is deprecated, and older versions (3.00 on GL.iNet's firmware)
  only speak that. The client probes with a JSON-RPC 2.0 `session_get`; a
  reply without `"jsonrpc": "2.0"` means the old protocol. Both encodings are
  in the same methods, and torrent fields decode from either spelling. Both
  need the CSRF handshake: a 409 carries `X-Transmission-Session-Id`, to
  store and resend.
- **When a download is ready to import:**

  ```
  ready = percent_done == 1 and (seed_ratio == 0
                                 or upload_ratio >= seed_ratio
                                 or is_finished)
  ```

  `is_finished` means Transmission stopped seeding by its own limits.
  Torrents are matched to import items by `download_dir` and `name`. Those
  skip the "changed in the last minute" check, since Transmission knows when
  they're complete. A torrent is removed from Transmission (keeping its data)
  only once something from it has been filed, so an unmatched download keeps
  seeding.
- **Adding** sets `download_dir` to the import folder (or its `movies/` or
  `tv/`), then sets the torrent's own seed ratio to `seed_ratio`.
- **Naming a download:** by its hash. Transmission's ids are given afresh
  each time it starts, so the website's buttons carry the hash, and the
  API's `{id}` takes either (`isDownload`).
- **Unreachable Transmission:** import warns and carries on.
- **Paths:** mvd and Transmission are assumed to see the same paths.
- **Polling cost.** The Incoming page polls every five seconds, and auto-import
  every minute. `trackerStats` are most of each answer, so each torrent
  decodes into a typed struct (no map of raw fields, which would copy them
  first) and each tracker into a small one; the body is read at its
  `Content-Length`, about 55 KB for six season packs, and a poll makes
  about 80 KB of garbage all told; and requests ask
  for `identity` encoding, so Transmission doesn't set up a 256 KB
  compressor for every answer.

### Transmission on OpenWRT

`mvd setup transmission` (in `internal/torrent/torrent.go`) prints UCI
commands. These are the reasons for each part, from OpenWRT's
`net/transmission` package and a GL-AR750S with GL.iNet's firmware 4.x
(OpenWRT 22.03, Transmission 3.00):

- **`config_dir` and the download folders default to `/tmp`**, which is RAM.
  The setup moves them to the drive.
- **`config_overwrite '1'`:** the init script rewrites `settings.json` from
  UCI at every start, so settings made over RPC are lost. Configure through
  UCI. Per-torrent settings (`torrent_set`) live in resume files and persist.
- **The procd jail** mounts only parts of `config_dir` and `download_dir`.
  An `incomplete_dir` beside `download_dir` is missing inside the jail, so
  partial downloads fail with "No data found!". Hence `import/.downloading`.
  So `torrent_set_location` into the library can't work either, and a
  "torrent done" script probably can't call mvd. mvd polls the RPC instead.
- **mbedTLS:** `transmission-daemon` is built against mbedTLS 2.28 but
  doesn't depend on it, and the firmware ships only OpenSSL. The setup checks
  with `ld-musl --list` and installs `libmbedtls12`.
- **exFAT can't `chown`**, so the `transmission` user can't write to the
  drive. The setup detects exFAT, FAT and NTFS from `/proc/mounts` and runs
  it as root.
- **The seccomp filter** leaves out `ftruncate64`, which Transmission 3.00
  calls to preallocate each new file. Its default action kills the process,
  and with no audit in the kernel, the daemon just vanishes. The setup adds
  `ftruncate` and `ftruncate64` (and a few `get*id` calls). To find a missing
  syscall: set the filter's default action to `SCMP_ACT_TRAP`, run
  `echo 1 > /proc/sys/kernel/print-fatal-signals`, and dmesg shows "fatal
  signal 12" (SIGSYS on MIPS) with the syscall number in `$2`.
- **Keep the jail.** Without it, procd applies the filter through ptrace
  instead, which can leave a stopped orphan daemon holding port 51413.
- **Memory:** the piece cache is set to a 64th of `MemTotal` (2 MB on
  128 MB), and the global peer limit to one per 3 MB (20 to 200). The queue
  is `max_downloads`. Fewer downloads at once saves little by comparison,
  since queued torrents are loaded anyway.
- **RPC** is bound to `127.0.0.1`. It's open to the LAN with no
  authentication by default.
- **Peers:** Transmission runs on the firewall itself, so UPnP doesn't help.
  The firewall rule for port 51413 is printed commented out.
- **Moving the incomplete folder** later means moving the partial data and
  rewriting each torrent's resume file (bencoded, so the length prefixes too)
  with Transmission stopped.
- **`aria2`** is in the same package feed, as a fallback if Transmission
  ever has to go.

### Transmission on Alpine

`mvd setup transmission alpine` (in `internal/torrent/alpine.go`) is
picked where OpenRC is the init system (`/sbin/openrc-run`, as for
`mvd setup service`). Alpine's `transmission-daemon` package (Transmission
4.x) has an OpenRC script configured by `/etc/conf.d/transmission-daemon`:
`runas_user`, `config_dir`, `logfile` and `TRANSMISSION_OPTIONS`.

- **It runs as `mvd`** (see the decision log), and the setup chowns the
  state and download folders to it.
- **`settings.json`** is written whole, with the daemon stopped, since it
  rewrites the file when it stops. Transmission fills in the keys left out.
  An existing one is kept as `settings.json.old`.
- **Memory and queue** are sized as on OpenWRT (`memSizes`), as JSON keys
  rather than UCI options.
- **No jail and no seccomp filter**, so none of OpenWRT's workarounds.
  `import/.downloading` stays, so finishing a download is a rename on the
  same drive.
- **Peers:** behind a router, Transmission's own UPnP and NAT-PMP port
  mapping (on by default) applies.
- **exFAT** and the like: the setup can't chown, so it says to mount the
  drive with mvd's `uid` and `gid`.

## Uploads

`internal/server/uploads.go` (single files), `uploads_folder.go` (folders),
and `internal/upload` (the client side, used by `mvd upload`).

- **What's taken:** `library.Uploadable`, the same list on the server, the
  page (its picker's `accept`) and `mvd upload`: videos, `UploadExts`, and
  archives (`.zip`, `.rar`, `.r00` to `.r99`), with names (each folder's
  too) of at most 240 bytes, checked at the start. Archives are only unpacked
  by the import, with its checks, so an upload is never trusted further
  than a download.
- **The protocol** is like tus, but our own: start with `POST /api/uploads`
  (the same fingerprint finds an unfinished upload), `PATCH` pieces at
  `Upload-Offset`, and `GET` to find where it got to. The `.part` file's
  size on disk is the offset, so there's no second record to keep in step.
  A piece cut short keeps what arrived.
- **For the router:** the body goes straight to the file through one
  `upload_buffer` (256 KB), never a multipart form. Pieces only append,
  since setting the size first on exFAT means writing zeros
  (`upload_preallocate` does it anyway, for drives where it's quick, and
  then the offset is kept in the record). Each piece is synced before it's
  acknowledged.
- **Staging:** unfinished uploads live in the import folder's `.uploads`.
  It's on the same drive, so finishing is a rename; and it's hidden, so
  imports skip it. The server tells the importer which files are finished
  uploads (`Importer.SetComplete`), so they skip the one-minute settle.
- **Folders** go as a group. The client picks a group id (FNV-1a 64 of the
  files' paths, sizes and times), the same every time, and names each file
  by its path in the folder. Finished files wait in `.uploads/<group>.d`.
  When all `group_files` are in, the folder moves into the import folder in
  one rename. A file of the group already in answers `done` at start, so a
  second drop skips it. The page and `mvd upload` compute the same group id.
- **Finished uploads are remembered for a week** (`.uploads/done`, by
  fingerprint or group), so sending again is a no-op, not a duplicate.
- **Clearing up** is `sweepUploads`, at startup and hourly: uploads and
  folders untouched for a week, finished ones remembered longer, and a
  `.part` whose record was never written. It holds the start lock, since a
  start makes the `.part` before its record. Listing uploads only reads:
  it runs on every poll, `/api/status` included.
- **`internal/upload`** documents the protocol step by step, for writing a
  client in another language.

## Auto-import

`internal/server/autoimport.go`. Every minute, and when an upload finishes,
it imports if the import folder's signature (paths, sizes and times) has
changed since the last import, or if the last import left something waiting
(retried in a minute), seeding, or failed (retried in ten). So a folder
holding only items that need a person doesn't import every minute. Imports
that file nothing aren't logged.

- **One walk, `scanImport`,** gives the signature and each item in the
  folder (as the import sees them: each entry, and each in `movies/` and
  `tv/`) with its own signature, size and latest change. Hidden names are
  skipped as the import skips them, except a folder's `.mvd`: writing a
  hint is a change worth an import.
- **The last import's results are kept** in memory (`imports` in
  `import.go`): each item's `ItemResult`, which the website's Incoming and
  Needs you show, so the page doesn't ask TMDB on every poll. Each import
  replaces them. One that stopped short (TMDB out of reach, or the key
  refused) keeps the earlier results for the items it didn't get to, and
  says why it stopped. It keeps the trash's size too, which the import
  already sums when it empties the trash (`Importer.TrashSize`), rather
  than walking the trash for each page.
- **Failures are counted per item,** in a row, while the item's signature
  stays the same. After three (`failLimit`) it isn't retried until it
  changes: it waits for a person. An import that stopped short counts none.
- **TMDB's cache is pruned** when the server starts and daily after, as the
  commands prune it after each run, whether or not auto-import is on.

## Up Next

`internal/server/upnext.go`, the only place ranking lives. `upNextLimit`
(40), `continueBoost` and `rank()` are at the top, to tune without touching
the pages or the API. Clients show the list in the order given and never
re-rank it.

- **Reasons:** `continue` (part-way), `next` (a started show's next
  episode), `new` (a movie from the last 30 days, or a show with new
  episodes, as its next episode). One entry per movie or show; a show's
  part-watched episode beats its next one.
- **One next episode, never a special** (`showNext`). Up Next, the show's
  `next_episode_id`, and the show pages all use it, so they can't disagree.
  Part-way through a special, the show is still offered at its next regular
  episode. Past the last episode with some skipped, Up Next has nothing, and
  the pages offer the first unwatched one.
- **The next episode follows the latest watch,** not the furthest, so a
  rewatch works. Episodes marked watched within a minute of each other count
  as one go, ending at the furthest, so marking a season watched doesn't
  look like you've just watched an episode in the middle of it.
- **Removing hides, it doesn't clear.** `Hidden` is kept on the movie's or
  show's progress entry, beside its position. Playing a movie replaces its
  entry (so it's back); a show compares the hide time with its latest play or
  arrival.
- **`resume_after`:** less than a minute into something doesn't count as
  started.
- **Artwork choices** are the server's (`backdrop`, `row_thumb`,
  `shelf_image` on each entry), so every client shows the same pictures.

## Search

`internal/library/search.go` (`Index.Search`: matching and ranking, tested
on their own), with thin callers in `internal/server`: `shelf.search`
(`search.go`), which `GET /api/search` and the website's `/search` both
answer with, so every client finds the same things. Why it's on the server
is in the decision log ("Search on the server").

- **What's matched:** a movie's title, collection (`Set`) and directors,
  and its year; a show's title and year; an episode's title, and its
  show's. Not genres, plots or cast.
- **Folding** (`appendFolded`): lower case; accents off, by a table for
  Latin-1 and Latin Extended-A (é → e, ø → o, ß → ss, æ → ae), since the
  standard library has no normalization; combining accents dropped (a Mac
  names files decomposed); apostrophes dropped ("Ocean's" is "oceans");
  `&` as "and"; anything else that isn't a letter or digit is a word break.
- **Every word typed must start a word** of one of those fields, in any
  order, or be the year (a four-digit word). A word may also carry on
  where the word typed before it stopped, so "god fa" finds "Godfather"
  while "father" doesn't (decision log). Up to 8 words; a bit each in a
  mask, and a match is the full mask.
- **Ranking,** within each kind: the whole title (a leading "the", "a" or
  "an" aside), the title starting with what's typed, every word in the
  title, then a match that needed something else (collection, director,
  year, an episode's show). Ties keep the index's order: `SortTitle` for
  movies and shows, a show's seasons and episodes in order.
- **Capped at `SearchLimit` (40) of each kind.** Matches go into a bucket
  per rank, each cut off at 40, and the best 40 are read off: nothing is
  sorted, and a one-letter query keeps at most 160 pointers a kind.
- **Certifications:** `Search` takes the access's `allowed`, so what a
  token can't see isn't found, and doesn't use up the cap. An episode goes
  by its show's.
- **No garbage per title:** the query is folded once; each title is folded
  into one buffer (`search.buf`) that's reused for the whole search, and
  compared there. A search allocates the query, the buckets and the
  results: a few KB, whatever the library's size (`TestSearchGarbage`).
- **Time:** it folds every title on each search. `BenchmarkSearch` over a
  made-up library of 2,000 movies, 200 shows and 20,000 episodes takes
  about 3 ms on a 2.1 GHz Xeon. Not yet measured on the router. If it's
  over 50 ms there, keep folded titles in the index (a few hundred KB)
  rather than folding each time.
- **Clients wait for a pause in typing** (300 ms) and drop the request
  before, so the router sees a request per pause, not per letter.

## Bonjour

`internal/mdns`, wired up in `internal/server/bonjour.go`. A responder of
its own, standard library only, with the DNS wire format written by hand for
just what's needed.

- **Two halves:** the `engine` (`engine.go`) makes the decisions. It's
  given each packet with the time, the network it came in on and whether
  it was addressed to this machine alone, told when time has passed
  (`tick`), and says when it next needs waking (`next`). It has no sockets,
  clocks or goroutines, so its tests use made-up packets and times (the
  `rig` in `mdns_test.go`, and the `wire` in `probe_test.go`, a pretend
  network of several servers). The `Responder` (`responder.go`) is the
  plumbing: the socket, the list of networks, and one timer for the
  engine's next wake. What the engine remembers is per network (one address
  of one interface): fixed-size arrays, nothing allocated while it runs.
- **What it answers:** PTR for `_mvd._tcp` and `_services._dns-sd._udp`, SRV
  and TXT for the instance, A and AAAA for its host, and an NSEC for each of those
  two names saying what it has, so a question for anything else (an AAAA)
  is answered "no" and resolvers don't wait. Everything else is ignored. It
  says goodbye (TTL 0) on the way out.
- **Probing** (sections 8.1 and 8.2): on each network, after a random 0
  to 250 ms, it asks three times, 250 ms apart, for any record of the
  instance and of the host, with the records it means to use in the
  authority section. Hearing nothing, it announces twice, a second apart,
  and logs the `Bonjour: "name" on ...` line. Until then it answers
  nothing on that network. The probes ask for multicast answers (QM): see
  the decision log.
- **Conflicts** (section 9): a record in someone's response with one of
  the two names and different data. The same data is no conflict: it's the
  service's own packet looped back, and an A record with any of this
  machine's addresses counts as the same (another interface on the same
  network). So two servers on one machine share the host name and
  conflict on the instance, as their ports differ. A conflict while
  probing means the name's taken: the next one is tried ("GL-AR750S (2)",
  `mvd-gl-ar750s-2`), on every network, and logged. A conflict later
  means asking again for the same name; whoever else has it does the
  same, and the tiebreak settles it. After 15 conflicts with under ten
  seconds between them, each try waits five seconds.
- **The tiebreak:** someone else's probe for the same name, seen while
  probing. `contest` compares the two sets of records (by type, then
  data, names written out in full). The lower waits a second and asks
  again, by which time the other has the name and answers. A probe seen
  when the name's already the service's is answered at once, whatever the
  once-a-second limit says, unless its records are the service's own. Legacy one-shot queries (not from port
  5353) are answered straight back with their id and short TTLs.
- **Who a reply goes to:** the group, unless the query was sent to this
  machine alone (RFC 6762 section 5.5: the reply goes to its sender alone,
  always), or every question it answers asked for a unicast reply (the QU
  bit) and each answer has been multicast on that network within a quarter
  of its TTL (section 5.4). The engine keeps when each record was last
  multicast on each network. Where a packet was addressed comes from the
  socket's control messages: `IP_PKTINFO` on Linux and macOS,
  `IP_RECVDSTADDR` on the other BSDs (`dst_*.go`). On a system with
  neither, every query is taken to be the group's.
- **Known answers** (section 7.1): a query lists the answers its sender
  has already. One that's the same record as the service's (`own` compares
  the name, type and data), with at least half its TTL left, isn't sent,
  and the additionals follow what's left: a browse that knows the PTR gets
  no reply at all. At most 64 records of a packet are looked at.
- **Pacing** (section 6): a network has at most one multicast reply
  waiting, and questions that arrive meanwhile join it. It goes at once if
  every answer is a record only this service has; after a random 20 to
  120 ms if a PTR is among them (other servers answer the same question,
  and shouldn't all in the same instant); after 400 to 500 ms if the query
  was truncated (section 7.2), and known answers that arrive from its
  sender in that time are taken off. No record is multicast on a network
  more than once a second: an answer sent that lately waits for its second
  to be up, and an additional is left out. Unicast and one-shot replies
  aren't delayed or limited. Nothing runs, and no timer is set, while no
  one's asking.
- **Sharing port 5353** with the system's responder: `SO_REUSEADDR`, plus
  `SO_REUSEPORT` (which macOS needs). The SRV names a host of its own,
  `mvd-<machine>.local`, since the machine's name belongs to the system's
  responder. If the port can't be shared, the server logs "Bonjour off" and
  carries on.
- **Which networks** (`networks.go`): a network is one address of one
  interface in one family. Each private or link-local IPv4 address is one
  (filtered by `mdns_interfaces` and `address`), on a socket joined to
  224.0.0.251 on that interface; a query is matched to its network by the
  sender's subnet, and the answer gives that network's address and goes
  out of its interface (`IP_MULTICAST_IF`). An interface with a link-local
  IPv6 address is one more, on a second socket (`[::]:5353`, joined to
  `ff02::fb` by interface index), matched by the interface the packet
  came in on (`IPV6_PKTINFO`, or the sender's zone). Each has its own
  state: IPv4 and IPv6 are separate conversations, probed and paced
  apart. Either socket can be missing (a machine without IPv6).
- **Addresses:** every answer gives the interface's addresses in both
  families, whichever it came over: the A, and an AAAA for the link-local
  address and up to two unique local (ULA) ones, never a global one. The
  host's NSEC lists which of the two it has. No AAAA when the server
  listens on IPv4 alone (`address = "0.0.0.0"` or one IPv4 address).
- **Networks coming and going** (`watch*.go`): the system says when an
  address or an interface changes, and the networks are listed again a
  second later (one change comes as several messages). On Linux that's a
  netlink socket; "new link" messages that change no flags are ignored,
  as a Wi-Fi interface sends them for its scans and statistics. On macOS
  and the BSDs it's a routing socket, and only its address and interface
  messages count. Elsewhere, or if the socket can't be had, the networks
  are listed every minute. A new network is probed and announced on; one
  that's gone is forgotten.
- **TXT:** `version` (the build), `api` (the API's level), and
  `auth=token` when a token is set, so a client pairs (or asks for it)
  before trying.
- **Cheap to ask:** anyone on the LAN can send packets as fast as they
  like, and every device's mDNS traffic arrives here, responses too (to
  spot conflicts). At most 32 questions and 64 records of a packet are
  looked at, and names are compared where they lie in the packet
  (`nameAt`), not built: a packet that isn't for the service allocates
  nothing (a test checks). A read error is logged once and the responder
  keeps listening.
- **No build tag:** it costs nothing measurable in the standard build.
- **Finding a server** (`browse.go`): `mdns.Browse` is the client's side,
  for `mvd upload`, `progress`, `status` and `search` on a machine with no
  server and no `[client] server` (`cmd/mvd/find.go`: this machine's
  first, which is instant to rule out, then a one-second browse; one found
  is used, several are listed). It asks as a one-shot resolver does
  (section 5.1): the PTR question to the group from an ordinary port, on
  each private network. Responders answer that straight back with SRV, TXT
  and addresses, so there's no port to share, group to join or cache to
  keep, and nothing in it is particular to one system. A third of the way
  through it asks once for anything a reply left out. IPv4 only.
- **Trying it on a real network:** `MVD_TEST_NETWORK=1 go test
  ./internal/mdns -run Live -v` starts two responders with the same name
  (a made-up service type) and browses for them through real sockets: one
  takes "(2)", and both are found. It's not run otherwise: it needs a
  network that carries multicast, and a Mac's firewall asks about it.
- **Windows:** the responder's stubs say "not supported". Browsing works.
- **What differs by system** is in small files: `sockopt_*.go` (sharing
  the port, joining, sending), `dst*.go` (where a packet was addressed),
  `watch_*.go` (network changes), `reuseport_*.go`. `go vet` with `GOOS`
  set to darwin, freebsd and windows checks they still build.

The config keys say `mdns` (the protocol); the log says Bonjour (what people
know it as).

## Tracks (ffprobe)

`internal/library/probe.go` and `internal/server/probe.go`. When ffprobe is
on the `PATH` (looked up once, at the first probe), the server asks it about
each video file: the container, the duration and each track's kind, codec,
language, title and flags (default, forced, hearing-impaired, commentary).
Without ffprobe nothing is probed, the API has no `files`, and clients do
what they did before. The import's runtime tie-break uses the same `Probe`.

- **Only the headers:** `-probesize` and `-analyzeduration` of 1 MB and 1 s,
  and `-show_entries` for just the fields used, as compact JSON. Each of
  stdout and stderr is capped at 256 KB, and a probe at 30 s. One ffprobe
  runs at a time. `-protocol_whitelist file`, so a "video" that's really a
  playlist can't have it fetch what it names.
- **Only the file's own tags.** No counting lines and no guessing languages:
  a track without a language has none (`und` is dropped).
- **JSON, else ffprobe's description.** After reading the headers, ffprobe
  opens a decoder for each stream, and stops where one won't open. The
  router's only HEVC decoder is `hevc_v4l2m2m`, for hardware it doesn't
  have, so for HEVC files it prints `{` and exits 1. Its description of the
  file (`av_dump_format`, on stderr at `-v info`) comes before that, with
  the same fields: `parseDump` reads it when the JSON is incomplete. The
  exit status is ignored. A file neither describes is kept with no
  container, so it isn't tried again until it changes.
- **`probeVersion`** is saved with each probe; a probe from another version
  is dropped at the next scan and the file probed again. Bump it when
  probing gets better.
- **In the index:** `Movie.Media` (one for each of `Files`, nil where not
  probed) and `Episode.Media`, with the file's size and modification time
  when probed. `BuildIndex` takes what's been probed (`Index.Probed`) and
  keeps a file's probe while its size and time match, so a rescan probes
  only new or replaced files.
- **Probed after the scan, not in it:** about a second a file on the
  router, so a first pass over a big library takes minutes. `rescan` starts
  a background pass over `Index.Unprobed`, one file at a time; every two
  minutes and at the end it swaps in a copy of the index with the new
  probes (`Index.WithMedia`, which copies only the movies and shows that
  change, since handlers may be reading the old one) and saves it. A rescan
  stops the running pass and starts another from the new index; probes the
  old pass made since its last swap are carried into the new index.
- **In the API,** `files` is only in full views (and so in Up Next), and
  only when every part has been probed, so it always lines up with
  `streams`.
- **Testing:** `testutil.WriteClip` makes a 3 KB MKV with flagged tracks
  when ffmpeg is installed. Tests that need ffprobe or ffmpeg skip without
  them, and the test library's episode `S01E02` is a real clip when ffmpeg
  is there, so making the Apple apps' fixtures needs both.

## Security

`internal/server/guard.go`.

- **Cross-site requests:** any request that changes something is refused if
  `Sec-Fetch-Site` is `cross-site` or `Origin` is another host. GET is
  untouched, since images and video load cross-site, except the import's
  TMDB lookups (search, details and posters): each costs a request on the
  owner's key and a file in the cache.
- **DNS rebinding:** without a token, any request addressed to an unexpected
  `Host` is refused, reads included. IP addresses, bare names and local
  suffixes pass, and `host_names` adds real domains.
- **Credentials:** a token (Bearer, `?token=`), a signed request, a
  signed link or the website's cookie, in that order of precedence; one
  that's there and wrong is a 401 without trying the next. See
  Credentials, below.
- **Request bodies** are capped before `auth` runs (`limitBodies` in
  `server.go`): 64 KB for forms and JSON, 8 MB for adding a `.torrent`, none
  for upload pieces, which stream to the drive. Multipart is refused except
  where a `.torrent` is added: `r.FormValue` would otherwise parse one of
  any size, spilling it to `/tmp`, which is RAM on OpenWRT.
- **Untrusted input:** files from a torrent are written by strangers, and
  on OpenWRT the import reads them as root. Archives and the RAR reader
  are covered under Archives, ffprobe under Tracks.

### Access

`internal/server/access.go`. Two levels: the admin (`[server] token`) and
viewers (`[[server.tokens]]`), who can browse, play and keep their own
progress. With no token configured, everyone is the admin.

- **`access`** is what a request is allowed: a name, admin or not, the
  certifications it sees (`ratings`, lower-cased; nil for no limit, and
  `unrated` for things with none), and its progress store. One per token,
  built at startup (`newAccesses`), the admin's first.
- **`auth`** works it out once per request from the request's credential
  (`credentialed`, below), and puts it in the request's context; `sv.who(r)` reads it. `X-Mvd-As: NAME` from the admin
  swaps in that viewer's access, in this one place, so everything after
  sees exactly what the viewer would.
- **Who can use which route** is one table, `routeLevels`, from each
  pattern the server registers (in any build) to `public`, `viewer` or
  `admin`. `auth` asks the mux which pattern a request matches
  (`mux.Handler`) and looks it up: `public` needs no token, `admin` is a
  403 for a viewer, and a pattern that isn't in the table is refused (fail
  closed). A path that matches nothing goes on to the mux's 404.
  `TestRouteLevelsCoverRoutes` reads the source, as the OpenAPI test does,
  so the table and the registrations can't drift apart.
- **The shelf** is the library as one access sees it (`sv.shelf(r)`, or
  `sv.pageShelf(r)` for the website's pages, whose media paths aren't
  signed):
  `Movie`, `Show` and `Episode` are nil for anything hidden, and `Movies`
  and `Shows` leave it out. An episode goes by its show's certification.
  The view builders (`movieView`, `showView`, `upNext`, `newEpisodes` and
  the rest) are its methods, with the access's progress store, so nothing
  in a request's path reads `sv.ix` or `sv.state` directly. What's left
  reading the index directly is the admin's and the background's: rescans,
  imports, probing.
- **Hidden is a 404,** with the same body as something that isn't there,
  and isn't counted: `/api/status`'s library, the home page and the show
  pages count what the access sees. `TestHiddenByCertification` walks
  every `viewer` route in the table that takes an id, so a new one is
  covered or it fails.
- **Progress per token:** the admin's store is `state.json`, a viewer's
  `state-<slug>.json` beside it (the name lower-cased, spaces as `-`). All
  are loaded at startup, one goroutine flushes them all, and shutdown does
  too.

### Credentials

`internal/server/credentials.go` (the formats, the guess table),
`access.go` (`credentialed`), `pair.go`, and `webui.go` and
`webui_pair.go` for the website's side.

A request's access comes from the first of these it has. One that's
present and wrong is a `401`; the next isn't tried.

1. `Authorization: MVD1 …`: a signed request (the Apple apps).
2. `Authorization: Bearer <token>`
3. `?token=<token>`
4. `?k=…&sig=…`: a signed link, on the media routes (`linkRoutes`) only.
5. The `mvd_session` cookie.

An Apple app from before signed requests adds `?token=` to the signed
URLs the API now gives it; rule 3 comes before rule 4, so it keeps
working. A presented token is compared against every one, constant time
each, with no early exit (`match`). Every `401` carries
`WWW-Authenticate: MVD1`, so a client can tell a server that knows the
scheme from an older one; a page a browser asked for goes to `/login`
instead, with `?next=` to come back to.

**Keys.** Each token's are derived once, at startup (`deriveKeys`), and
kept on its `access`; `sv.kids` finds an access by key id. Nothing is
derived per request.

**Signed links** are what the API hands out: with a token, every media
path in a JSON response (`poster`, `fanart`, `thumb`, `stream`, `streams`,
each subtitle's `url` and `vtt`, Up Next's pictures) is signed with the
request's link key, by the shelf's `linkSigner`. A client uses them as
given. The website's pages build the same views from `pageShelf`, unsigned,
since the browser has the cookie; its `.m3u` links (`playLink`) and
everything in an `.m3u` are signed, so no page or playlist has the token
in it. A link handled is exactly as if its token had been sent: that
token's access, and its certification filter, so a link to something since
hidden is a 404. Signing reuses one MAC and buffer per request: a list of
500 movies (1,000 signatures) takes about 5 ms under qemu-mips against
0.4 ms unsigned, and one allocation per link beyond the path itself
(`BenchmarkMovieListSigned`).

**Signed requests:** `auth` reads the body (at most `formBodyMax`, else
`413`) to hash it, and puts it back for the handler. It checks the
signature first, then the time; a good signature with a stale time is a
`401` with `error="stale"` and the server's time in `Date`, and isn't a
failed guess. There's no record of signatures seen: a captured request can
be sent again for up to five minutes.

**The cookie** is the token's key id and a MAC (`keys.cookie`), worked out
at startup, so checking it is a lookup and a constant-time compare.
Signing in sets it and clears `mvd_token` (the old cookie, which held the
token, and is no longer read). A cookie that doesn't check out is cleared,
so a browser whose token changed isn't counted as guessing page by page.
`POST /logout` clears it.

**Limiting guesses** (`guesses`): one fixed table of 64 addresses, so a
flood of addresses can't grow it, behind a mutex, with no goroutine and no
allocation. A credential that's present and wrong counts (a bad token,
signature or cookie, a wrong token at `/login`, an unknown pairing code);
none at all, or a stale time under a good signature, doesn't. After 5 in a
row an address is locked out for a second, doubling to a minute; while it
is, anything from it that presents a credential is a `429` with
`Retry-After`, without the credential being checked. A success clears the
address. When the table is full, the entry whose last failure is oldest
makes room. The address is the connection's, or, when that's loopback and
there's an `X-Forwarded-For`, its last entry (a reverse proxy on the same
machine).

**Pairing** (`pair.go`) gives a device a token's key id and request key
without the token being typed on it. Waiting requests live in a map behind
a mutex, pruned whenever it's touched: at most 8, 2 per address, nothing
evicted to make room, each for 5 minutes (`sv.clock()`). An approval makes
a new X25519 key pair and seals the device's keys for it; the box is
handed out once and forgotten. The website's Pair page (`/pair`, with
`webui`) and `mvd pair` both approve through the same `approvePair`,
which is given the approver's access: any token can let a device in as
itself, and only the admin can name another (a `403` otherwise). Whoever
knows a token could type it on the device, so this grants nothing new.
`POST /api/pair`'s reply names the Pair page (`"page": "/pair"`), empty
without `webui` (`pairPagePath` in `webui_none.go`), so a device with no
page to point at says to run `mvd pair` instead. It's always in the reply:
the apps take a reply without it to be from an older server, which had
`/pair`.
The list of what's waiting (`GET /api/pair`, and on the page) is the
admin's alone. Pairing is in every build: its crypto (X25519, HKDF,
AES-GCM) is linked in for TLS anyway, so what it adds is its own code. The
credentials and pairing together are about 130 KB of the router build.

#### The formats

Everything is HMAC-SHA256 (`crypto/hmac`, CryptoKit's `HMAC<SHA256>`).
"b64" is base64url without padding (`base64.RawURLEncoding`). Comparisons
are `hmac.Equal` or `subtle.ConstantTimeCompare`, always. The labels end in
`v1` so a format can change later.

With `token` as the UTF-8 bytes of the token as written in the config:

```
kid        = hex( HMAC(token, "mvd key id v1")[0:8] )
linkKey    = HMAC(token, "mvd link v1")
requestKey = HMAC(token, "mvd request v1")
cookieKey  = HMAC(token, "mvd cookie v1")
```

The key id says which token a signature or cookie belongs to without
naming it. Config names aren't used on the wire: an app given a token
can't know its name, and names have spaces. Three keys, one per use, so
that one leaking doesn't give the others, and none is the token.

**Signed link,** for `GET` and `HEAD` on `/stream/{id}`, `/subs/{id}/{n}`,
`/art/{id}/{kind}` and `/play/{file}` only:

```
/stream/abc123?part=2&k=<kid>&sig=<sig>

sig = b64( HMAC(linkKey, "GET\n" + target)[0:16] )
```

- `target` is the escaped path (`r.URL.EscapedPath()`), then `?` and the
  raw query if there is one, with the `k=` and `sig=` parameters taken
  out: split the raw query on `&`, drop the parts starting `k=` or `sig=`,
  join the rest with `&` in the order they came. So `part=2` is signed and
  can't be changed.
- `HEAD` is signed as `GET`.
- The signature is cut to 16 bytes (128 bits): a list of 500 movies
  carries a thousand of them.
- No expiry. If one is ever wanted, it's `&exp=<unix seconds>` before `k`,
  which puts it inside `target` with nothing else to change.

**Signed request,** for any route:

```
Authorization: MVD1 k=<kid>,t=<unix seconds>,sig=<sig>

sig = b64( HMAC(requestKey,
        "MVD1\n" + METHOD + "\n" + target + "\n" + t + "\n" + hex(SHA256(body))) )
```

- `target` is the escaped path and raw query, as sent. `METHOD` is upper
  case. `body` is the request's body, empty for none (`e3b0c442…b855`).
  `sig` is the full 32 bytes.
- `t` must be within 300 seconds of the server's clock, either way. The
  server's clock is the reference: a travel router has no battery-backed
  clock and no NTP on the road, so the device's can't be compared with it.
  The client takes the server's time from the `Date` header (every
  response has one; a stale `401`'s is from `sv.clock()`), remembers the
  difference, signs again and tries once more.
- Bodies up to 64 KB only; uploads use Bearer.

**Website cookie:**

```
Set-Cookie: mvd_session=<kid>.<b64( HMAC(cookieKey, "mvd session v1") )>
```

`HttpOnly`, `SameSite=Lax`, `Path=/`, a year, `Secure` when the browser
reached the server over HTTPS (or a proxy says so with
`X-Forwarded-Proto`). It signs a browser in and nothing else: it isn't the
token, and can't be used as `?token=`, a Bearer header or a signing key.
Changing the token signs every browser out.

**Pairing:**

```
device                           server                         a signed-in browser
  | POST /api/pair  {public_key, device}
  |------------------------------->|  keeps it (5 min), works out the code
  |<------- 201 {code, expires_in} |
  | shows the code, and a QR of http://<server>/pair?code=<code>
  |                                |<------ GET /pair?code=…  (or /login first, then back)
  |                                |------- the device's name, the code (admin: the tokens)
  |                                |<------ POST /ui/pair  {code}  (admin: token: "kids")
  | GET /api/pair/{code}           |
  |------------------------------->|
  |<-- 200 {server_key, nonce, sealed}   (once; then it's forgotten)
  | opens it: {k, name, admin, request_key}
```

- The code is the first 5 bytes of `SHA256(public_key)` in Crockford
  base32 (`0123456789ABCDEFGHJKMNPQRSTVWXYZ`), 8 characters, shown as
  `CC6W-TAB6`; accepted with or without the hyphen, in either case.
- The box:

  ```
  shared    = X25519(server's new private key, device's public key)
  key       = HKDF-SHA256(secret: shared, salt: device_key || server_key,
                          info: "mvd pair v1", 32 bytes)
  sealed    = AES-256-GCM(key, nonce, plaintext, additional data: the code)
  plaintext = {"k": "<kid>", "name": "kids", "admin": false,
               "request_key": "<b64, 32 bytes>"}
  ```

  `crypto/ecdh`, `crypto/hkdf`, `crypto/aes` and `crypto/cipher`.
- `GET /api/pair/{code}` is `202 {"status": "waiting"}` until approved,
  and `404` for unknown, expired, refused or already collected (counted as
  a wrong guess).

#### Test vectors

Go's tests (`credentials_test.go`, `pair_test.go`) reproduce these, and the
Apple apps' must too.

```
token        correct-horse-battery-staple-0123
kid          e9ec59536a0993e9
linkKey      a47ba02af92c7633c9ba7afa1ae0a8ebddc0adcd3bcf4d5b8ccf8fd5acc93c1c
requestKey   a00c2101f85eb91c2a50ee679b514a1d3151d6938c0d1d66225773bfa9a2bcb0
cookieKey    8b150cff8d8675e6e8afbe12236d34564a2f627b23d717609cd046aad726860a

link         GET /stream/abc123?part=2
             sig = 8qQTFwxKREAY-S0A_w7W5A

request      GET /api/movies?since=30d, t=1794400000, no body
             sig = DkIaVFZG9Ig2rqYgkk92ncldcCemKf43uuI0NPUg-uQ

request      POST /api/progress/abc123, t=1794400000,
             body {"position":120,"duration":5400}
             SHA256(body) = a13660cae5de859f0564cacbc5ffe1cba6ac383015a0e438f4a6a4e2e4c8de23
             sig = dyHCfX1MegRvwWQnoRVuEaz4d7gRxwfx7hkFRnj9Nw0

cookie       e9ec59536a0993e9.wDvNAP6ukgSHCPs4m32YMUzenzFJ6Ve6QPJd_5OjFug

pairing      public key = bytes 0x00 to 0x1f
             code = CC6WTAB6
```

What a review (November 2026) checked and found sound, so nobody re-checks
it without a reason:

- **Paths:** `/stream`, `/subs` and `/art` only open paths the index holds,
  looked up by id. Upload names go through `filepath.Base` or a part-by-part
  check with `..` and hidden names refused. `importItem` checks `Within`,
  then requires the path to be an item the scan found. Archive entries are
  flattened to a base name, and links are never made or followed.
  `/static/../` can't dodge `auth`: the mux redirects or serves the
  embedded files.
- **Templates:** all `html/template`, no `template.HTML`, no data in script
  blocks; the one `innerHTML` in `incoming.js` is a constant, with names set
  through `textContent`.
- **Token comparison** is constant-time, and **JSON bodies** are all behind
  `MaxBytesReader`.
- **The trash purge** only deletes names in its own date-stamped form, and
  `CheckTrashDir` refuses a trash that overlaps the library.
- **The import lock's takeover** (rename, then remove) is race-free.

## Memory

Measured on a copy of the router's library with `GODEBUG=gctrace=1` and
`-memprofilerate 1`, and on the router through `/api/status` and `/proc`
(October 2026):

| Workload | Allocated | Collections | Heap at most |
| --- | --- | --- | --- |
| Startup scan (13 movies, 16 shows, 411 episodes) | 13 MB | 4 | 4 MB |
| Import of an 85-episode season, with art and NFOs | 7 MB | 3 | 4 MB |
| A 400 MB upload, 256 KB buffer | — | 5 | 3 MB |
| A poll of the downloads list (6 torrents) | 0.08 MB | — | — |

- **The heap stays at Go's 4 MB minimum goal,** with 1 to 3 MB live. On the
  router, RSS is 13 to 19 MB, but about 8 MB of that is the binary's code
  (`RssFile`), which the kernel drops under pressure. `RssAnon` is about
  7.5 MB.
- **`GOMEMLIMIT=15MiB`** in the procd script makes Go collect harder near the
  limit, leaving room for Transmission.
- **What used to spike,** and was fixed: decoding Transmission's tracker
  stats (see Downloads), and the scan listing each episode's whole folder
  for its sidecars (the square of the season's length). Each folder is now
  listed once.
- **Small, and left alone:** `Index.Save` marshals the index (about 280 KB,
  plus about 0.5 KB a file once probed: a 6-track file's tracks are 540
  bytes of JSON, so about 230 KB more for 440 files) and reads the old file
  to compare, about 2 MB of garbage per scan, and per swap during a probe
  pass; TMDB
  answers (up to 175 KB) are read whole; `POST /ui/downloads` parses a form
  with up to 8 MB in memory, though `.torrent` files are small.
- **What's fetched is capped:** TMDB answers at 8 MB and images at 32 MB,
  Transmission's answers at 16 MB, and an `.srt` converted to WebVTT at
  8 MB. A wait TMDB asks for (`Retry-After`) is at most ten seconds.
- **The kernel:** OpenWRT's watermark boosting raised the free-memory reserve
  from 6 MB to about 20 MB after fragmentation, and the out-of-memory killer
  went off with 17 MB free. `mvd setup memory` turns boosting off.

## The HTTP API

[`api/openapi.yaml`](../api/openapi.yaml) is the contract: every field,
auth and error. A test checks it lists exactly the routes the server
registers, so update it with any API change. After an API change,
[mvd-apple](https://github.com/potato-video/mvd-apple)'s
`scripts/fixtures.sh` regenerates the Apple apps' test fixtures (real
responses from the test library, by `TestWriteAppleFixtures`); commit
them there.

| Endpoint | |
|---|---|
| `GET /api/up-next` | the ranked list: each movie or episode with a `reason` (`continue`, `next`, `new`) and, for shows, `new_count` |
| `POST /api/up-next/{id}/remove` | take a movie, episode or show out of Up Next |
| `GET /api/movies`, `/api/shows` | the lists, by title; `/api/movies?since=30d&unwatched=true` for new movies |
| `GET /api/episodes/new[?since=30d]` | per show, the first new unwatched episode, and how many there are |
| `GET /api/search?q=god+fa` | `movies` and `shows` as the lists have them, and `episodes` in brief, best first, at most 40 of each (see Search); nothing typed is three empty lists |
| `GET /api/item/{id}` | a movie, a show (with seasons and episodes) or an episode, by any id; a movie's or episode's `files` once probed (see Tracks) |
| `GET /api/movies/{id}`, `/api/shows/{id}` | the same, for a movie or a show; a show carries `next_episode_id` |
| `POST /api/progress/{id}` | `{"position": s, "duration": s}` from a player (`{"position": 0}` clears it), or `{"watched": bool}`; a show id marks every episode |
| `POST /api/rescan` | read the library again |
| `GET /api/status` | build, uptime, memory, CPU, the library's counts and size (as the token sees it, the size from the index's stat of each video), the free space on its drive, uploads under way, which token (`access`) |
| `POST /api/import` | import what's ready, no questions asked; returns counts and the report |
| `GET /api/import` | what the last import left for a person (Needs you), with each question's candidates |
| `GET /api/import/search`, `/details` | TMDB by words or a reference; a title's details and what importing an item would file |
| `GET /api/import/poster/{kind}/{id}` | a title's poster, small, streamed through the server |
| `POST /api/import/choose` | `{"item", "sig", "kind", "tmdb"}`: write the item's hint and start an import |
| `POST /api/import/retry`, `/trash` | `{"item"}`: try a failed item again; move one to the trash |
| `/api/uploads`, `/api/uploads/{id}` | the upload protocol (see Uploads) |
| `GET /api/downloads` | Transmission's downloads, with their state in words and whether they're ready |
| `POST /api/downloads` | add one: `{"source": "magnet:...", "kind": "movie"}`, or a form with a `torrent` file |
| `POST /api/downloads/{id}/pause`, `/resume` | pause or resume; `all` for every one |
| `POST /api/downloads/{id}/verify` | check a download's data and start it again (`mvd torrent verify`) |
| `POST /api/downloads/{id}/move` | `{"position": n}`: to that place in the queue, from 0 |
| `DELETE /api/downloads/{id}` | cancel a download, or stop seeding a finished one |
| `POST /api/pair`, `GET /api/pair/{code}` | a device asks to be let in, and collects its keys (see Credentials) |
| `GET /api/pair` | what's waiting (admin) |
| `POST /api/pair/{code}/approve`, `DELETE /api/pair/{code}` | let a device in, as the token asking (`{"token": NAME}` for another: admin); refuse it |
| `GET /stream/{id}[?part=N]` | the video, with range support |
| `GET /subs/{id}/{n}[.vtt]` | sidecar subtitles; `.vtt` converts SRT for browsers |
| `GET /art/{id}/{poster,fanart,thumb,season-N}` | artwork |

- **Media paths in responses are signed links** when there's a token
  (see Credentials): a client uses them as given, resolved against the
  server's address.
- **Ids are stable:** `m238` (TMDB movie), `s95396` (show), `e…` (TMDB
  episode), `f…` for files without metadata.
- **Every response has `X-Mvd-Version`,** the server's build (`git
  describe`). `mvd version` compares it with its own, since replacing the
  binary doesn't restart the server.
- **The API only grows.** No route, field or value is removed or renamed,
  or changes its meaning or type; a new field is optional to read, and a
  new request field defaults to the old behavior. If something has to
  change, the new form goes beside the old one, which stays. Clients skip
  what they don't know, so a new field, `kind` or `reason` is safe.
- **Every response has `X-Mvd-Api`,** the API's level (`server.APILevel`,
  also in `/api/status` and the Bonjour TXT): raise it by one, in the same
  commit, with each addition a client may rely on, and say "API level N"
  where the addition is described in `api/openapi.yaml`, whose
  `info.version` is the same number (a test checks). A server without the
  header is level 1. Clients decide by the level, never by the build
  (`dev` and forks' tags don't order): the Apple apps hide what the
  server's level is too low for, and refuse one below their minimum.
- **Clients sort the lists themselves.** The whole list comes in one
  response, so the server only gives the fields to sort on: `year`,
  `added` and `last_watched` on each movie and show, `runtime` on movies
  and `unwatched` on shows. Ties keep the title order. A show's `added` is
  its newest episode's, and its `last_watched` its latest episode's,
  specials included. `last_watched` comes from the progress entry's
  `played`, which only playing or marking watched sets: removing from Up
  Next or clearing the position changes `updated`, not `played`, so
  something taken out of Up Next keeps its place. Entries saved before
  `played` existed use `updated`.
- **An import or refresh** on the same machine tells a running server to
  rescan (`POST /api/rescan`), trying again after 2, 4, 8 and 16 seconds
  if that fails (`pingServer`). A refused connection means no server is
  running, so it doesn't retry.

## The website

The web UI is `internal/server/webui*.go`, with templates in `ui/templates`
and static files in `ui/static`, all built into the binary. It's
server-rendered `html/template` with [htmx](https://htmx.org/), and a little
plain JavaScript (`home.js` for Up Next, `incoming.js` for uploads and drops). No build
step and no framework.

- **It matches the Apple TV app,** while staying a website: the `.m3u` link
  under Play, hover to clear progress, each episode's own Play and watched
  buttons. The same wording ("Play" / "Resume", "Play S01E04", "Mark as
  Watched", an episode's "S01E02 · 3 Mar 2022 · 36m left", Up Next's
  "Continue watching" with "3 new" beside it) and the same button looks
  (gold Play; cornflower Watched and Remove from Up Next).
- **Lengths are worked out as in MVDKit** (`timeLeft`, `secondsLeft`): the
  NFO's runtime, else the length played before; once started, what's left
  is that length less the share watched, so it agrees with the length
  shown. A line says one or the other, never both.
- **One hero:** the `hero` and `playactions` partials (`partials.html`) are
  the Apple app's `HeroView` and `PlayActions`: poster (not on Up Next),
  Up Next's pills, title with the age rating after it (a word joiner keeps it on the
  last line), an episode's line, facts with the genres last (and first, the
  original language when it isn't the viewer's: `viewerLang` takes the first
  `Accept-Language` tag, English without one; `lang.Name` names it), the plot, then
  the progress line over the buttons, and the link under them. `heroData`
  and `playData` (`webui.go`) carry them.
- **Up Next** renders every entry's feature up front, all but one hidden.
  `home.js` swaps them on click or focus (never hover), plays on a second
  click or Enter, and keeps the selection across htmx re-renders.
- **The grids sort as the Apple apps do** (`webui_sort.go`, after
  mvd-apple's `LibraryOrder` in MVDKit): the same sorts and words, natural
  directions (A to Z, shortest first, otherwise newest or most first), ties
  in title order,
  and anything with nothing to sort on last either way. `?sort=added` (or
  `-added`, reversed) picks the order and saves it in a cookie per grid
  (`mvd_sort_movies`, `mvd_sort_tv`), which a plain `/movies` then uses.
  The select swaps in the library through htmx; the arrow is a link to the
  reverse. `rerender` passes the cookies on, so a clear on a grid keeps
  its order.
- **Search** (`webui_search.go`, `search.html`) is `/search?q=`, from the
  magnifying glass at the end of the header: `shelf.search` called
  directly, drawn as the grids' cards under Movies, TV and Episodes. A
  plain `<form method="get">`, so it works without JavaScript; with it,
  the field asks again 300 ms after typing stops (`hx-trigger="input
  changed delay:300ms, search"`), drops an answer still on its way
  (`hx-sync`), and swaps in only `#results`, so it keeps its focus. The
  address is replaced, not pushed (`hx-replace-url`): a search can be
  bookmarked, and Back leaves the page rather than untyping it letter by
  letter. An episode links to `/tv/{show}?season=N#c-ep-{id}`, the anchor
  its row already has, and `.ep:target` marks the row; those links aren't
  boosted, since a browser only sets `:target` on a real page load.
- **Signing in** (`/login`) sets the session cookie and goes back to
  where the browser was going (`?next=`, a local path only). The page
  asks for a "password" (decision log: "Password" to people), and when
  it's on the way to `/pair?code=…` says which device signing in will
  let in. The footer names the token, with Sign out and a link to Pair
  (`webui_pair.go`, `pair.html`): with `?code=`, the device and the code
  in large type to check against its screen, and one button to let it in
  with this browser's access. The admin, with viewers' tokens configured,
  chooses which token instead, none chosen to start with, and without a
  code sees what's waiting.
- **Toggles re-render:** marking watched forgets the position, so the watched
  button redraws the hero (and an episode's row) through `rerender`, as
  clearing progress does.
- **Parts:** one Play; the watch page plays the parts in turn, keeps the
  first part's position only, and marks the movie watched at the end of the
  last.
- **htmx boosting keeps the old `<body>`:** it swaps the body's contents, so a
  body class from the page you came from sticks, and the arriving page's is
  lost. So nothing hangs off body classes: the top bar has a fixed `--top-h`,
  pages use `calc(100dvh - var(--top-h))`, and bare pages (the player) aren't
  boosted.
- **Incoming** (`incoming.go`) is one list built on the server from three
  sources: unfinished uploads (a folder as one row; the speed is measured
  from where the server first saw it going), Transmission's downloads
  (`incomingTorrents`, in the downloads build: the queue in its order, then
  seeding, then done), and the import folder's items (`scanImport`) that no
  download claims, with the last import's result for each. An item an
  import left for a person, or that has failed `failLimit` times, or a
  download Transmission reports a problem with, goes to Needs you instead.
  Problems with everything (Transmission out of reach, an import that
  stopped short) are banners, once, not on each row.
- **One poll:** the page polls `/ui/incoming` every five seconds (not while
  a download is being dragged), which answers with the
  list and Needs you, and, out of band, the head's figures and the nav's
  count. Buttons answer with the same, plus a line saying what they did.
  Polls within four seconds of a look share it (`incomingShare`), so open
  tabs don't each walk the import folder and ask Transmission; a button's
  answer looks afresh, and is what the next polls share.
  Each row's i (details) is a `<details>` kept across refreshes (`hx-preserve`).
- **Needs you** (`needs.go`, the API; `webui_needs.go`, the page) answers
  what an import left by writing the item's `.mvd` hint and starting an
  import, which files it under its lock: the page never files anything.
  One hint settles one movie or one show, so only an item whose question is
  the only thing in it (or that holds only videos the import couldn't
  place) can be answered here. An answer names the item and its signature
  (`scanImport`'s), and is refused if the item has gone or changed, or an
  import is running here or at a terminal. Each card is kept across
  refreshes (`hx-preserve`), so a search's results stay; its id includes
  the item's signature, so a changed item gets a fresh card. The dialog's
  details are one TMDB lookup, which the import that follows reads from the
  cache, and its poster is streamed through the server at `w185` and not
  kept, so the browser never loads from TMDB.
- **`incoming.js`** keeps uploads going across htmx page swaps, since
  boosting keeps the document. The server lists every unfinished upload,
  not knowing whose is whose; the page knows its own by upload id or folder
  group (remembered in `localStorage`, for "drop it again to carry on"
  after the tab closed), and after each refresh gives those its own finer
  progress, moves them after the rest, and adds rows for those still
  waiting to start. A web page can't reopen a file after the tab closes, so
  the fingerprint (name, size, modified time) finds the unfinished upload
  when it's dropped again. The whole page takes drops; a loose `.torrent`
  goes to `POST /ui/downloads` instead.
- **Reordering the queue** is pointer events, not the browser's drag and
  drop: the row lifts out of the list solid (the browser's would be a faded
  picture), a line shows where it will land, and it works by touch (by the
  handle, so a finger elsewhere scrolls). It sends the download it now sits
  before (or after, at the end), not an index, since seeding downloads have
  queue positions too.
- **Escaping:** values only ever go into text or double-quoted attributes.
  Never into `<script>`, `style=` or unquoted attributes. (The progress
  bar's `style="width:N%"` takes an integer, and must stay that way.)
- **The typeface** is Bricolage Grotesque (OFL), in `ui/static`.
- **TMDB's credit:** TMDB's API terms ask whatever shows its data to say so,
  with its logo. Every page but the player's has a footer with the logo
  (`ui/static/tmdb.svg`, the Apple TV app's, TMDB's own) linking to
  themoviedb.org, and the notice (`tmdb.Notice`); the import dialog shows
  the logo too. `mvd version` prints the notice. Pages that fill the screen
  leave the footer below the fold.

If the website grows enough to need it, this section can move into its own
WEB_DEVELOPMENT.md.
