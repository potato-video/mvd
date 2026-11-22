# mvd: notes for Claude

mvd (mini video daemon) is Warren's small, low-resource media server for a
movie and TV collection: a Go server and CLI (import and TMDB matching, a
JSON API, streaming, a web UI, Transmission downloads). Its native Apple
apps (Apple TV, iPhone and iPad) are a repo of their own,
[mvd-apple](https://github.com/potato-video/mvd-apple). **It runs on a
potato:** the reference machine is a GL-AR750S OpenWRT router (big-endian
MIPS, 128 MB RAM, no swap) with the library on an exFAT USB drive. Weigh
every change against that. It isn't Plex or Jellyfin, and isn't trying to
be. The README explains the use case.

## The docs

| Doc | What it's for |
|---|---|
| [README.md](README.md) | what mvd is, and installing and running the server |
| [docs/SETUP_OPENWRT.md](docs/SETUP_OPENWRT.md), [docs/SETUP_ALPINE.md](docs/SETUP_ALPINE.md) | setting mvd up on each kind of machine, from building to Transmission and SMB |
| [docs/CONFIG.md](docs/CONFIG.md) | every setting in config.toml, explained |
| [docs/COMMANDS.md](docs/COMMANDS.md) | every `mvd` command and its flags |
| [docs/DOWNLOADS.md](docs/DOWNLOADS.md) | downloads: setting up Transmission, `mvd torrent` |
| [docs/FAQ.md](docs/FAQ.md) | common questions and problems, debugging |
| [docs/CLIENTS.md](docs/CLIENTS.md) | using the web UI, the Apple TV app, other players |
| [docs/PRIVACY_POLICY.md](docs/PRIVACY_POLICY.md) | what mvd keeps and sends; shown in the Apple TV app's Settings, so keep it true |
| [docs/SERVER_DEVELOPMENT.md](docs/SERVER_DEVELOPMENT.md) | the Go code: layout, build tags, testing, style, how each part works, the API, the website |
| [api/openapi.yaml](api/openapi.yaml) | the HTTP API contract |
| [LICENSE.md](LICENSE.md), [CONTRIBUTING.md](CONTRIBUTING.md) | the MIT License (shown in the apps' Settings too), and what contributors agree to |

**The planning docs are private, in
[mvd-assets](https://github.com/potato-video/mvd-assets)** (checked out
beside this one, at `../mvd-assets`), because this repo is public. They're
every repo's, the Apple apps' included: nothing that's planning goes in
this repo, only the code and the docs about it as it is.

| Doc | What it's for |
|---|---|
| [ROADMAP.md](../mvd-assets/docs/ROADMAP.md) | what's planned, one tagged item each (`[Server]`, `[Web]`, `[Server/AppleTV]`, `[AppleTV]`, ...) |
| [DECISION_LOG.md](../mvd-assets/docs/DECISION_LOG.md) | the bigger decisions and why: read it before reopening one |
| [TERMINAL_DEVELOPMENT.md](../mvd-assets/docs/TERMINAL_DEVELOPMENT.md) | the planned terminal client (a placeholder) |
| [VISUAL_DESIGN.md](../mvd-assets/docs/VISUAL_DESIGN.md) | the web UI's look: palette, type, layout |
| `../mvd-assets/docs/features/*.md` | working notes for a roadmap item, deleted when it's merged |

Don't link to them or quote them from this repo's docs, code comments,
commits or PRs: on GitHub those links are broken for everyone else. Say
"it's planned" at most.

Read the README and the development doc for the part you're working on
first. Check the decision log before proposing something that sounds like
it may have been tried.

**Keep the docs current, in the same branch as the change:**

- Behavior people see: README, the SETUP guides, CONFIG, COMMANDS,
  DOWNLOADS or FAQ (the server and CLI), or CLIENTS.md (the clients).
- How it works: the matching `*_DEVELOPMENT.md`.
- A choice between real alternatives, or something tried and dropped: an
  entry in mvd-assets' DECISION_LOG.md.
- Finishing a roadmap item: remove it from mvd-assets' ROADMAP.md, fold
  what's worth keeping from its `features/` file into the docs above, and
  delete the feature file.
- Starting a bigger item: write `../mvd-assets/docs/features/<NAME>.md`
  and link it from the item.
- A change in mvd-assets is a commit there, on a branch of its own.
- **Downloads stay in docs/DOWNLOADS.md.** The README, FAQ, CLIENTS and
  CONFIG are what a reviewer may read from potato.video, so they mention
  the `downloads` feature only to point there: no Transmission setup,
  troubleshooting or settings in them.
- Docs say how things are, not their history. Git has the history, and the
  decision log has the reasons.

## Working with Warren

- Show changes as diffs or before/after blocks, not whole files.
- Plain, direct prose in code comments, commits, docs and UI text.
- **American English** in code, comments, commits and docs (color,
  behavior, movies). Existing British spellings in older code
  needn't be rewritten, but new and changed text is US.
- Fix bugs test-first: reproduce with a failing test, then fix.
- Work on a feature branch off `main`, one per piece of work (Warren usually
  names it). Commit there, not to `main`. `main` takes PRs: ask before
  opening one, or merging.
- Never commit secrets: tokens, TMDB keys, configs with them in.

## Go (server and CLI)

Details in docs/SERVER_DEVELOPMENT.md.

- Go 1.24, standard library only, no cgo. Layout: `cmd/mvd` (one file per
  command), `internal/{config,library,tmdb,torrent,upload,mdns,server,testutil}`.
- Tools past a shell script are Go too (not Python), each in a module of
  its own under `scripts/`: they may use a few modules, and import nothing
  of mvd's (`go test ./...` there). `mvd-demogen`, the
  demo library's maker, is in a repo of its own
  ([mvd-demogen](https://github.com/potato-video/mvd-demogen)).
- Features are build tags: `webui`, `files` (uploads, the Incoming page,
  Needs you), `downloads`, `unpack` (all on by default in the Makefile;
  `go build` with no tags gives the minimal build, enough for a library
  filed with `mvd import`). A `!tag` file
  holds the stubs. Keep the router binary small: `make sizes`.
- `make test` vets and tests every tag combination: run it before
  committing. `go test -race` for anything concurrent. `make router` for the
  MIPS build.
- A `v*` tag makes a release (`make release`: builds without `downloads` or
  `unpack`, and `scripts/install.sh`). potato.video is the `mvd-site` repo,
  which App Review may read: nothing about downloads there. It copies
  `docs/PRIVACY_POLICY.md`, the web UI's typeface and `scripts/install.sh`
  (its `sync.sh`): after changing one, sync and commit there. Keep the
  installer in step with the SETUP guides.
- `testutil` must stay standard library only (every package's tests use it).
- `api/openapi.yaml` documents the HTTP API; a test checks it lists exactly
  the routes the server registers. Update it with any API change.
- After an API change, mvd-apple's `scripts/fixtures.sh` regenerates the
  Apple apps' test fixtures (real responses from `TestWriteAppleFixtures`);
  commit them there. mvd-apple copies the privacy policy, the web UI's
  typeface and `style.css` (its `scripts/sync.sh`): after changing one,
  sync and commit there too.
- A new setting goes in `internal/config`'s table and in
  `deploy/config.example.toml` (a test checks they match), and is
  explained in docs/CONFIG.md (`[downloads]`'s in docs/DOWNLOADS.md).
- Memory: no multipart forms for large bodies (Go spills them to `/tmp`,
  which is RAM on OpenWRT), stream rather than buffer, and mind garbage on
  anything that polls.

## The Apple apps

They're [mvd-apple](https://github.com/potato-video/mvd-apple), with a
CLAUDE.md of their own. What the server does for them stays here: the API
(`api/openapi.yaml`), the fixtures' test (`TestWriteAppleFixtures`),
`scripts/test-server.sh` (a throwaway server with a long Up Next, website
included, on `:6838` with token `test`, which their UI tests run against),
and mvd-assets' roadmap and decision log entries that span both
(`[Server/AppleTV]`, signed requests, pairing). The apps' own roadmap and
decisions are in mvd-assets too. No downloads or torrent features in the
apps.

## Where things are

What's done is described in the docs; what's next is mvd-assets'
ROADMAP.md, roughly in
order of interest. Don't keep a separate status list here.
