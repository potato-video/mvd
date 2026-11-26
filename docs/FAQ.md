# FAQ and debugging

Common questions and problems. Commands are in [COMMANDS.md](COMMANDS.md),
settings in [CONFIG.md](CONFIG.md), and downloads in
[DOWNLOADS.md](DOWNLOADS.md).

## Where's the log?

`logread -e mvd` on OpenWRT, `grep mvd /var/log/messages` with OpenRC
(Alpine), `journalctl --user -u mvd -f` with systemd,
`~/Library/Logs/mvd.log` with the LaunchAgent. In a terminal, `mvd server`
logs to the terminal.

## I installed a new mvd, but nothing changed

The running server is still the old binary. `mvd version` compares the two.
Restart the service.

## mvd says "running as root" or "no TMDB API key", but the server works

mvd reads `~/.config/mvd/config.toml` for whoever runs it, so as root it
reads root's config, not the server's, and leaves files the server can't
move or trash. Except on OpenWRT, where the service runs as root, run mvd's
commands as the user the service runs as: `su mvd -c 'mvd refresh'` on
Alpine, and without `sudo` with systemd or on a Mac. The "no TMDB API key"
message names the config it read.

## The browser won't play a file

Browsers play MP4 with H.264 reliably, and MKV in Chrome; most other
formats, not at all. mvd doesn't transcode, so use the `.m3u` link on every
movie and episode page to play it in VLC, mpv or IINA, which play anything.
The Apple TV, iPhone and iPad app plays everything through VLC.

## Something didn't import

Run `mvd import` at a terminal to see why, and answer its questions. Or add
a `.mvd` hint file (see [`mvd import`](COMMANDS.md#mvd-import)). Common causes: the
file is still arriving (wait a minute), it's already in the library, or it's
TV without episode numbers in its name (put it in `import/tv/`).

## An archive wasn't unpacked

`mvd import` says why. The usual reasons:

- **This mvd was built without it.** Unpacking is the `unpack` feature, in
  the standard build; `mvd version` lists a build's features.
- **It's compressed.** mvd reads *stored* RARs (packed with `-m0`, how
  nearly all video releases come) by itself. For compressed ones it uses
  `unrar` if it's installed: `opkg install unrar` on OpenWRT (it brings
  `libstdcpp` with it, if the firmware hasn't got it), `apt install unrar`
  on Debian and Ubuntu. Zips are always fine.
- **A volume is missing or damaged.** A set needs every volume, and each
  part is checked against its CRC. Download it again, or the missing part.
- **There isn't room.** Unpacking needs room for what's inside, beside the
  archives. mvd says how much.
- **It needs a password**, or it's a `.7z` or a split `.001` file: mvd
  doesn't unpack those. Unpack it yourself, in `import/`.

Archives aren't unpacked twice: `.mvd-unpacked` in the folder lists what
was. Delete it to have them unpacked again.

## A show's episodes are in the wrong order or have the wrong titles

The files probably follow DVD or production order. See
[`mvd refresh`](COMMANDS.md#mvd-refresh) and `-order`.

## mvd keeps getting killed

On a 128 MB router with no swap, the kernel kills the biggest process when
memory runs out. Check:

- `GOMEMLIMIT=15MiB` is in mvd's procd script (`mvd setup service` puts it
  there).
- You've run `mvd setup memory` (watermark boosting off).
- `dmesg` shows the kernel's out-of-memory reports, if there are any.

`mvd status -every 5s` shows mvd's own memory as it goes. Running the server
with `GODEBUG=gctrace=1` (in the procd script's `env` line) logs each
garbage collection.

If downloads are set up, see [DOWNLOADS.md](DOWNLOADS.md#problems) for
their problems, the download client being killed among them.

## The app doesn't find the server

Check that the server is announcing (`logread -e mvd` shows a
`Bonjour: ...` line per network), that the Apple TV, iPhone or iPad is on
the same network, and that the network passes multicast (some mesh Wi-Fi
and guest networks don't). You can always type the address on the setup
screen instead.

On an iPhone or iPad, the app also needs Local Network: iOS asks the first
time it looks. If it was refused, the setup screen says so, with a button
to the app's page in Settings; a typed address works either way.

## Something's missing for one token

A token with `ratings` sees only those certifications, spelled as the
library has them: open the movie as admin and check its certification
against the list. Things with no certification are hidden unless the
token has `unrated = true`. An episode goes by its show's. See
[CONFIG.md](CONFIG.md#tokens).

## Up Next is different on different devices

Each token has its own watch history. Devices that should share one
need the same token.

## Is a link from mvd safe to share?

An `.m3u` link, or a stream address from one, plays that one file for
anyone who has it, until you change the token. It doesn't give away the
token or anything else in the library. An address you wrote yourself
with `?token=` on it gives away the token.

## "Too many tries"

Five wrong tokens in a row from one address and the server makes that
address wait, a little longer each time, up to a minute. Wait, then use
the right one. A device with an old token keeps trying it: sign it in
again.

## The app asks me to sign in again

The token it was signed in with has changed, or been taken out of the
config. Type the new one as the password, or on an Apple TV scan the
square code with a phone that's signed in to the website. If the app says the server needs
updating, the server is older than the app: build and install the current
mvd.

## Is it safe over plain HTTP?

On a home network you trust, it's fine. Otherwise, know what plain HTTP
gives away:

- **What's watched,** and the library, to anyone who can read the network.
- **The token, when it's sent:** signing in to the website sends it once,
  in the form, and so does anything using `?token=` or a Bearer header
  (`mvd upload` from another machine, an mpv command you wrote). The
  website's cookie and the `.m3u` links don't contain it.
- **Signed links:** someone who sees one can play that file until the
  token changes.
- **The Apple apps' signed requests** don't contain the token or
  anything that makes more of them; a captured one can be sent again for
  five minutes.

For anything beyond a network you trust, put mvd behind a reverse proxy
with HTTPS (see ["Before you expose it"](../README.md#before-you-expose-it)).
