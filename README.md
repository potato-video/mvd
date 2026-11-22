# mvd

**mvd** (*mini video daemon*) is a small, low-resource server for browsing
and watching your own video collection. It's built to run on a potato: the
reference machine is a 2018 GL.iNet GL-AR750S travel router (a 775 MHz single core MIPS chip, 128 MB of RAM) with a hard drive plugged into its single USB 2.0 port,
formatted exFAT so it can also go straight into a Mac or a PC. 

## Why it exists

- **On the road.** Put mvd on a travel router with an external drive, and
  you have a box of movies and TV for car trips and hotel rooms, where Wi-Fi
  is slow, expensive or missing. The family connects to the router and each can
  watche from a tablet, a laptop or an Apple TV, with no internet needed.
- **At home.** Put it on the router you already run, with a drive full of
  your movies and TV, and you get a friendly way to browse them.
  Streaming services don't have everything, and what they have can disappear or move to
  services you don't subscribe to. Your discs may be in a box in a storage
  unit, but your own copies can still be watched, presented as nicely as any streaming service.
- **No extra box.** You don't need a dedicated always-on computer (maybe with a
  fan running all the time) just to serve some video files.
- **Just video.** mvd doesn't want to manage your music (use mpd), podcasts,
  photos or anything else. It does movies and TV.

mvd isn't Plex, Kodi or Jellyfin, and isn't trying to be. It has no transcoding,
no user accounts and no plugins. It sends files as they are and leaves
decoding to the player. In exchange it's one small binary with no
dependencies, using a few MB of memory. **Running on a potato comes first.**
Every feature is weighed against that.

## What it does

- **Imports.** Drop files in an `import/` folder (over SMB, by upload,
  however). mvd works out what each one is, matches it on
  [TMDB](https://www.themoviedb.org/), renames it into a tidy library, and
  writes Kodi/Jellyfin-style NFOs and artwork. Your library stays readable
  by other software and by people.
- **Serves.** A JSON API, streaming with seeking, subtitles, artwork, and
  watch progress kept on the server, so any device picks up where another
  left off.
- **Up Next.** One list of what to watch: what you're part-way through, the
  next episode of shows you've started, and what's new.
- **Clients.** A web UI built into the server, and native apps for the
  Apple TV, iPhone and iPad.
  Any player that opens a URL (VLC, mpv, IINA) can play a file from its
  `.m3u` link. See [docs/CLIENTS.md](docs/CLIENTS.md).
  - **Uploads.** Send files to the library from a browser or another machine, resumable after a dropped connection.


## How light is it?

Measured on the GL-AR750S (a 775 MHz MIPS chip, 128 MB of RAM):

| Build | Binary |
|---|---|
| minimal (import, refresh, API, streaming, uploads) | 8.0 MB |
| + web UI | 10.4 MB |
| standard (every optional part) | 10.4 MB |

- **Memory:** about 8 MB of private memory while running. The Go heap stays
  at its 4 MB minimum through a library scan, a large import or a 400 MB
  upload. The total resident size is 13 to 19 MB, but about 8 MB of that is
  the binary's own code, which the kernel can drop and read back as needed.
- **CPU:** close to idle while streaming, because files are sent as they are,
  never transcoded.
- **Disk:** a library index, a TMDB cache and a watch-progress file, a few MB
  in total. Uploads are written straight to the drive, never through RAM or
  `/tmp`.
- **Nothing else:** one static binary, no runtime, no database, no
  dependencies.

## Contents

- [Install](#install)
- [Configure](#configure)
- [The library](#the-library)
- [Commands](#commands)
- [Optional parts](#optional-parts)
- [On your network](#on-your-network)
- [FAQ and debugging](#faq-and-debugging)
- [More documentation](#more-documentation)

## Install

On Linux (an OpenWRT router, a Raspberry Pi, a server), the installer does
it all. Run it on the machine mvd is for:

```bash
wget -O install.sh https://potato.video/install.sh && sh install.sh
```

It fetches the [latest release](https://github.com/potato-video/mvd/releases/latest)'s
build for the machine, asks where your videos are and for a TMDB key,
writes the config and sets mvd up as a service. Run it again later to
update mvd. On a router, mount the drive first
(see [the OpenWRT guide](docs/SETUP_OPENWRT.md#mount-the-drive)).

The release builds have the web UI and uploads. For the other
[optional parts](#optional-parts), or for a Mac, build mvd yourself. It needs Go
1.24 or later and nothing else (no cgo, no other modules), and it
cross-compiles from any machine.

```bash
git clone https://github.com/potato-video/mvd.git
cd mvd
make            # mvd for this machine
```

### What you need

- **A machine that's on.** Your router, a Raspberry Pi, a NAS, or your
  laptop. Linux, macOS and other Unix-likes work. Memory and CPU figures from
  `mvd status` are Linux only.
- **A drive for the library.** Any filesystem works. exFAT is fine (it's
  what the reference router uses), and mvd works around its limits.
- **A TMDB API key.** It's free: see [Getting a TMDB key](#getting-a-tmdb-key).

### Setting it up

A guide for each kind of machine, from building to sharing the drive:

- **[An OpenWRT router](docs/SETUP_OPENWRT.md)**, the reference setup: a
  GL.iNet travel router with a USB drive.
- **[Alpine Linux](docs/SETUP_ALPINE.md)**, on a Raspberry Pi, with mvd
  running as its own user.

Elsewhere, `mvd setup service` prints a service file for the machine it
runs on, with this mvd's path filled in:

- **Linux with systemd** (Raspberry Pi OS, a NAS, a server): a systemd
  *user* unit that runs as you, with the install steps at the top. Build
  for a Pi with
  `make router ROUTER='CGO_ENABLED=0 GOOS=linux GOARCH=arm64' ROUTER_OUT=mvd-linux-arm64`
  (or `GOARCH=arm GOARM=7` for 32-bit).
- **macOS:** a LaunchAgent.
- **Anything else:** run `mvd server` however you start long-running
  programs. It only needs its config file and the library.

`mvd setup service procd|openrc|systemd|launchd` picks a kind explicitly.
The originals are in [`deploy/`](deploy/).

### Updating

Run the installer again, if that's how mvd got there. Otherwise, build again, copy the new binary over the old one, and restart the service
(`/etc/init.d/mvd restart` on OpenWRT, `rc-service mvd restart` on Alpine). `mvd version` tells you whether the
running server is the binary you just installed, or an older one still
waiting for a restart.

## Configure

The config is `~/.config/mvd/config.toml` for the user mvd runs as (on
OpenWRT, `/root/.config/mvd/config.toml`). `mvd setup config` prints a commented one
with every setting and its default, to start from. A minimal config for the
router:

```toml
[paths]
video_dir = "/mnt/drive/video"   # holds movies/, tv/, import/ and .trash/
data_dir  = "/mnt/drive/.mvd"    # mvd's own files: on the drive, not the router's flash

[tmdb]
api_key = "your-key-here"
```

**[docs/CONFIG.md](docs/CONFIG.md) explains every setting:** the library's
folders, auto-import and the trash, the server's address, token, uploads
and Bonjour, TMDB's language and region, and the client commands.

### Getting a TMDB key

mvd gets titles, plots, artwork and episode lists from The Movie Database.
A key is free for personal use:

1. Create an account at [themoviedb.org](https://www.themoviedb.org/signup)
   and confirm your email address.
2. Go to **Settings → API**
   ([themoviedb.org/settings/api](https://www.themoviedb.org/settings/api))
   and request a key. Choose **Developer**, accept the terms, and fill in the
   form: personal, non-commercial use of a home media library is what it's
   for.
3. Copy either the **API Key** (the short v3 key) or the **API Read Access
   Token** (the long v4 token) into `api_key`. mvd accepts both.

TMDB answers are cached under `data_dir` for 30 days, so mvd only asks again
for what's new.

This product uses the TMDB API but is not endorsed or certified by TMDB.
The key is yours, under TMDB's terms; the website shows TMDB's logo and
this notice at the foot of each page, and `mvd version` says it too.

## The library

```
video/
  movies/    Title (Year)/Title (Year).mkv
  tv/        Show (Year)/Season 01/Show (Year) - S01E02 - Title.mkv
  import/    drop anything here
  .trash/    what's left over from imports
```

mvd writes to `movies/` and `tv/` itself. Everything new comes in through
`import/`. Alongside each video go its NFO, its artwork and its subtitles,
named the way Kodi and Jellyfin expect. If you ever switch, they can read
the library as it is.

## Commands

```
mvd import [-n] [-y] [PATH]        bring new things into the library
mvd import -copy PATH              the same, copying from elsewhere
mvd refresh [movies|tv] [flags]    tidy what's already there
mvd server [-address A] [-port N]  serve it
mvd status [-json] [-every 5s]     how the server is doing
mvd search WORDS...                find movies, shows and episodes
mvd progress list|show|clear|watched
mvd upload [-movie|-tv] PATH...    send files to a server, from another machine
mvd setup config|service|memory|token
mvd version
```

**[docs/COMMANDS.md](docs/COMMANDS.md) explains each command and its
flags:** how imports decide what things are, hint files, the trash,
auto-import, episode orders, reading `mvd status`, and the rest.
`mvd <command> -h` lists a command's flags.

## Optional parts

The web UI, uploads, downloads and unpacking archives are
optional, chosen when building. Leave out what you don't need to keep the
binary small. The minimal build is enough to keep a library you fill
yourself (over SMB, say) and file with `mvd import`.

| Feature | What it adds | Size on the router |
|---|---|---|
| (always) | import (asking at a terminal), refresh, auto-import, the API, streaming, Bonjour | 8.4 MB |
| `webui` | the web pages | +2.4 MB |
| `files` | uploads (`mvd upload`, `/api/uploads`), the Incoming page (with `webui`), and answering what an import left (Needs you) | +0.2 MB |
| `downloads` | downloads, through a download client on the same machine (see [docs/DOWNLOADS.md](docs/DOWNLOADS.md)) | +0.2 MB |
| `unpack` | unpacking `.zip` and `.rar` archives on import (and uploading them, with `files`) | +0.2 MB |

The features are independent, but `downloads` and `unpack` help get things in,
so without `files` they lose their part of the website and archive uploads,
and `make` warns.

```bash
make                     # the standard build: webui files downloads unpack
make PRESET=minimal      # none of them
make FEATURES="webui"    # any mix
make sizes               # what each adds to the router binary
make release             # what a release holds, in dist/: webui and files, for each kind of machine
```

Plain `go build -tags 'webui files downloads unpack' ./cmd/mvd` works too. With no tags,
you get the minimal build. `mvd version` lists what a binary was built with.

## On your network

### Found by Bonjour

The server announces itself on the local network over mDNS/DNS-SD (Bonjour,
as `_mvd._tcp`), so the Apple apps list it on their setup screens. Check it
with `dns-sd -B _mvd._tcp` on a Mac, or `avahi-browse _mvd._tcp` on Linux.
It shares port 5353 with the system's own responder (umdns or avahi on
OpenWRT, mDNSResponder on a Mac) and doesn't interfere with it.

```toml
[server]
#mdns            = true            # false: don't announce
#mdns_name       = "Living room"   # what the apps show; the machine's name by default
#mdns_interfaces = "br-lan"        # only on these, comma-separated; every private network by default
```

(See [docs/CONFIG.md](docs/CONFIG.md#bonjour).)

It only ever answers on private networks, never on a public address. On a
router whose WAN side is also a private network (behind another router),
set `mdns_interfaces = "br-lan"` (the GL.iNet LAN bridge; `ip addr` on the
router lists the others) to keep it off the WAN. `logread -e mvd` shows
where it's announcing:

```
mvd: Bonjour: "GL-AR750S" on br-lan (192.168.8.1)
```

(one line per network, and one more for an interface with IPv6, with its
`fe80::` address). Before that it asks the network whether another
server has the name already. If one does (two routers of the same model,
say) it takes the next: `"GL-AR750S (2)"`, and the log says so. Give each
its own `mdns_name` to tell them apart.

It answers over IPv4 and IPv6. It's off when `address` is `127.0.0.1`. If it can't share the port, the log
says `Bonjour off: ...` and the server carries on without it.

### Before you expose it

mvd is built for a home network, or a travel router's own Wi-Fi. Out of the
box there's **no sign-in at all**: anyone who can reach the port can browse,
stream, upload, start imports and (with `downloads`) add downloads. Before
it's reachable from anywhere you don't trust:

- **Set `token`** to something long and random (`mvd setup token` makes
  one). Every request then needs it. The website and the Apple apps ask
  for it once, as the password; an Apple TV can be signed in from a phone,
  with nothing to type on the TV.
- **Give other people their own tokens.** A token under
  `[[server.tokens]]` can watch but not upload, download or import, can
  be limited to chosen certifications, and has its own watch history. See
  [docs/CONFIG.md](docs/CONFIG.md#tokens).
- **Put it behind a reverse proxy with HTTPS** (Caddy, nginx) if it's
  reachable from anywhere you don't trust. Over plain HTTP, anyone on the
  network can see what's watched, and the token itself when you sign in
  to the website. The sign-in cookie is marked Secure when the proxy sends
  `X-Forwarded-Proto: https`.
- **Links handed to players don't carry the token.** An `.m3u` link plays
  that one file and nothing else, until the token changes. A URL you
  write yourself with `?token=` on it does carry the token, so keep those
  out of shared history and logs.

Without a token, mvd still refuses changes from other websites through your
browser (cross-site requests), and any request addressed to a domain name
it doesn't expect (DNS rebinding). IP addresses and local names (`router`,
`nas.lan`) are always fine. `host_names` lists any real domain you reach it
by.

There are no user accounts. A token is a key to the library, not a
person: a device has one, and what's watched with it is that token's
history.

## FAQ and debugging

[docs/FAQ.md](docs/FAQ.md) has the common questions and problems: where
the log is, files the browser won't play, things that didn't import,
episodes in the wrong order, running out of memory on a router, and the
Apple apps not finding the server.

## More documentation

- [docs/SETUP_OPENWRT.md](docs/SETUP_OPENWRT.md) and
  [docs/SETUP_ALPINE.md](docs/SETUP_ALPINE.md): setting mvd up on a router
  or a Pi, from building to sharing the drive.
- [docs/CONFIG.md](docs/CONFIG.md): every setting in config.toml.
- [docs/COMMANDS.md](docs/COMMANDS.md): every command and its flags.
- [docs/DOWNLOADS.md](docs/DOWNLOADS.md): optional downloads (the `downloads` feature).
- [docs/FAQ.md](docs/FAQ.md): common questions, problems and debugging.
- [docs/CLIENTS.md](docs/CLIENTS.md): using the web UI, the Apple TV, iPhone
  and iPad app, and other players.
- [docs/SERVER_DEVELOPMENT.md](docs/SERVER_DEVELOPMENT.md): working on the
  server, the web UI and the API.
- [mvd-apple](https://github.com/potato-video/mvd-apple): the Apple apps, a repo of
  their own: building and working on them.
- [`api/openapi.yaml`](api/openapi.yaml): the HTTP API, for writing clients.

## License

mvd is open source, under the [MIT License](LICENSE.md): the server, the
command line and the Apple apps alike. You can use, change and share it,
in anything, as long as the copyright and license notice go with it.

"mvd" and its icon are Warren Merrifield's. Forks are welcome, under another
name and icon (see [CONTRIBUTING.md](CONTRIBUTING.md)).
