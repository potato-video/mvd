# Configuration

Every setting in mvd's config file, what it does, and its default. For
installing mvd, see the [README](../README.md#install).

## The file

mvd reads `~/.config/mvd/config.toml` (or `$XDG_CONFIG_HOME/mvd/config.toml`).
On OpenWRT that's `/root/.config/mvd/config.toml`, since the procd script
sets `HOME=/root`. Every command reads the same file, the server included.
Change it, then restart the server.

To start one, print the commented example, which has every setting and its
default ([`deploy/config.example.toml`](../deploy/config.example.toml)):

```bash
mvd setup config > ~/.config/mvd/config.toml && chmod 600 ~/.config/mvd/config.toml
```

It never reads your current config, so don't redirect it over a file you
want to keep.

A minimal config, for the router:

```toml
[paths]
video_dir = "/mnt/drive/video"
data_dir  = "/mnt/drive/.mvd"

[tmdb]
api_key = "your-key-here"
```

### How it's written

It's a small subset of TOML:

- **Every setting goes under its `[section]`.** As in TOML, every line after
  a section header belongs to that section.
- `key = value`, one per line. Strings can be quoted (`"..."` or `'...'`) or
  bare. Quote anything containing `#`.
- `#` starts a comment, at the start of a line or after a space.
- **Paths** may start with `~/`.
- **Booleans** are `true` or `false`.
- **Durations** are like `"36h"`, `"7d"` or `"1m"` (Go's units, plus `d` for
  days). `0` means none.
- **Sizes** are like `"500MB"`, `"2GB"` or `"256KB"`. The units are powers of
  1,024: KB, MB, GB, TB (`KiB` and the like work too).
- **A setting mvd doesn't know,** or one under the wrong section, is ignored
  with a warning saying where it belongs:
  `mvd: config: unknown setting [server] api_key (it belongs under [tmdb])`.
  A bad value is ignored too, with a warning, and the default stays.

## `[paths]`

Where the library is, and where mvd keeps its own files.

### The library

| Setting | Default | |
|---|---|---|
| `video_dir` | (none) | the library's folder, holding the four below |
| `movies_dir` | `video_dir/movies` | where movies are filed |
| `tv_dir` | `video_dir/tv` | where TV is filed |
| `import_dir` | `video_dir/import` | where new things are put to be imported |
| `trash_dir` | `video_dir/.trash` | where imports' leftovers go |

**`video_dir`** is the one to set. The other four default to folders inside
it, and each can be set on its own to override that (with no `video_dir`,
set `movies_dir` and `tv_dir` at least). Without either, the server has
nothing to serve, and `mvd import` nowhere to file things.

**`import_dir`** should be on the same drive as the library. Filing a video
is then a rename, which is instant. Across drives, it's a copy and a delete,
which is slow over USB.

**`trash_dir`** can't contain, or sit inside, the library or the import
folder. mvd refuses one that does, since the trash is emptied by itself.

### mvd's own files

| Setting | Default | |
|---|---|---|
| `data_dir` | `~/.config/mvd` | mvd's own files: the three below, `import.log` and `import.lock` |
| `cache_dir` | `data_dir/cache` | TMDB's answers, kept for 30 days |
| `state_file` | `data_dir/state.json` | watch progress and Up Next |
| `index_file` | `data_dir/index.json` | the library index, so the server starts quickly |

**`data_dir`** moves them all at once. On a router, put it on the drive
(`/mnt/drive/.mvd`, say): the state file is rewritten every few seconds
while something plays, and the router's flash is small and wears out. It
also keeps your watch history safe across firmware upgrades.

**`state_file` is the only one worth backing up.** It holds the watch
history. The cache and index are rebuilt when missing (the cache from TMDB,
the index by scanning the library).

## `[library]`

How the library behaves.

| Setting | Default | |
|---|---|---|
| `auto_import` | `true` | the server imports by itself |
| `resume_after` | `"1m"` | how far into something before it counts as started |
| `trash_max_age` | `"7d"` | leftovers older than this are deleted |
| `trash_max_size` | `"2GB"` | past this, the oldest leftovers are deleted first |

**`auto_import`:** the server imports whatever arrives in
`import_dir`: finished uploads at once, finished downloads once they've
seeded, and anything else once it's been unchanged for a minute. It checks
every minute, and only imports when something has changed. What it isn't
sure of stays in `import_dir` for `mvd import` at a terminal (or a `.mvd`
hint file). With `false`, imports happen when you run `mvd import`, press
Import now on the Incoming page, or call `POST /api/import`: for anyone who'd
rather import at a terminal.

**`resume_after`:** stopping less than this far into a movie or episode
doesn't count as starting it, so a quick look doesn't put it in Up Next as
"continue watching".

**`trash_max_age` and `trash_max_size`:** the trash is emptied against both
at the start of every import. Either set to `0` turns the trash off, and
leftovers are deleted straight away. Only entries mvd put there are ever
deleted.

## `[server]`

How `mvd server` listens, and who it lets in.

| Setting | Default | |
|---|---|---|
| `address` | `""` (every interface) | the address to listen on |
| `port` | `6837` | the port to listen on |
| `token` | `""` (none) | the admin's password: every request must carry a token, or something made from one |
| `host_names` | `""` | domain names the server is reached by, comma-separated |
| `[[server.tokens]]` | none | more tokens, which can only watch: see [Tokens](#tokens) |

**`address`:** empty listens on every interface. `"127.0.0.1"` is this
machine only (behind a reverse proxy on the same machine, say), and turns
Bonjour off. An IP address listens on that one only. `mvd server -address`
overrides it. The command-line commands on the same machine (`mvd status`,
the rescan after `mvd import`) find the server at this address, or at
`127.0.0.1` when it's empty, `0.0.0.0` or `::`.

**`port`:** `mvd server -port` overrides it. The command-line client
commands on the same machine use it too, to find the server.

**`token`:** the admin's token. With it set, every request must carry a
token, this one or one of the [viewers'](#tokens), as `?token=` or an
`Authorization: Bearer` header, or something made from one: the website's
sign-in cookie, a signed link, or an Apple app's signed request (see
[What a token protects](#what-a-token-protects)). The website asks for it
once, as the password, and so do the Apple apps (or an Apple TV is let in
from a phone: see [CLIENTS.md](CLIENTS.md#connecting)). Use something long and
random: `mvd setup token` prints one. Under 16 characters, the server warns
when it starts, and `mvd status` says so. Without a token, anyone who can
reach the port can do anything. See
["Before you expose it"](../README.md#before-you-expose-it). The client
commands (`mvd upload`, `progress`, `status`, `pair`) send this token, so
on another machine, set it to the server's.

**`host_names`:** without a token, the server refuses requests (reading as
well as changes) addressed to a domain name it doesn't expect, which
protects against DNS rebinding. IP
addresses and local names (`router`, `nas.lan`, `mvd.local`) are always
fine. List any real domain you reach it by:
`host_names = "media.example.com"`. With a token, it isn't needed.

### Tokens

The `token` above can do everything. Each `[[server.tokens]]` entry is
another token, for a device or a person, that can only watch:

```toml
[server]
token = "long-random-admin-token"

[[server.tokens]]
name = "kids"
token = "another-long-random-token"
ratings = ["U", "PG"]
unrated = false

[[server.tokens]]
name = "living room"
token = "a-third-long-random-token"
```

Keep the entries last in `[server]`: the settings after a `[[server.tokens]]`
line are that entry's, until the next `[section]`.

| Setting | Default | |
|---|---|---|
| `name` | (required) | what the token is called: 1 to 32 letters, digits, spaces, `-` and `_` |
| `token` | (required) | the token itself |
| `ratings` | all | the certifications it can see |
| `unrated` | `false` | whether it sees things with no certification |

A viewer's token can browse, play, and keep its own watch progress. It
can't upload, download, import or rescan, and the website doesn't show it
those pages.

**`ratings`:** a list of the certifications this token may see, written
as your library has them (a movie's page shows its certification), so in
the system of `tmdb.region`: `["U", "PG", "12A"]` in Britain,
`["G", "PG", "PG-13"]` in the United States. It's a list, not a maximum:
list each one. An episode has its show's certification. Anything else
isn't there for this token: not in lists, not in Up Next, not by its
address. Leave `ratings` out and the token sees everything.

**`unrated`:** plenty of things have no certification. A token with
`ratings` doesn't see them unless this is `true`.

**Watch progress** is kept per token: the admin's in `state.json`, a
viewer's beside it (`state-kids.json`, `state-living-room.json`). The kids'
cartoons stay out of your Up Next, and yours out of theirs. Devices that
should share one history share one token. Taking a token out of the config
leaves its file where it is. `mvd progress -as kids` works on a viewer's.

The server won't start with viewers' tokens and no `[server] token`, an
entry without a name or a token, a name that's `admin` or two that would
share a progress file, or two tokens the same. It says which.

Changing a token signs out everything that used it: browsers, paired
apps, and every link it signed. `mvd setup token kids` prints a new entry,
with a new token, to paste in.

#### What a token protects

- **Links handed to players** (the website's `.m3u` links, and
  everything in them) are signed for that one file. They don't contain
  the token and don't expire: a saved playlist keeps working until the
  token changes, and someone who gets hold of one can play that file and
  nothing else.
- **The website's cookie** signs that browser in and can't be used for
  anything else. "Sign out", at the foot of each page, forgets it.
- **The Apple apps** never send the token. They sign each request with a
  key made from it when it's typed in as the password, or given to an
  Apple TV when it's let in from the website's Pair page (or with
  `mvd pair`), so the token needn't be typed on the TV at all. Any
  browser that's signed in can let a TV in, with its own access and no
  more; only the admin's can choose another token.
- **Wrong guesses** are slowed down: after five in a row from one
  address, that address has to wait, a second and then longer each time,
  up to a minute at a time. Behind a reverse proxy on the same machine,
  the address is the one the proxy passes on in `X-Forwarded-For`.
- **`?token=` and `Authorization: Bearer`** still work, for a URL typed
  into mpv, for `curl`, and for `mvd`'s own commands. They send the
  token itself.

None of this hides anything from someone who can read the network. Over
plain HTTP they can see what's watched, use a signed link they see, send
an app's request they captured again for five minutes, and read the token
when it's typed into the website's sign-in page. They don't get the token,
or a key, from the apps. For that,
put mvd behind a reverse proxy with HTTPS. The cookie's Secure flag comes
from `X-Forwarded-Proto`, which any client can send; the worst that does
is stop that client's own cookie working.

### Uploads

| Setting | Default | |
|---|---|---|
| `uploads` | `true` | uploads from the Incoming page and `mvd upload` |
| `upload_preallocate` | `false` | give a file its full size before the first piece |
| `upload_buffer` | `"256KB"` | the memory each piece is written through, 4KB to 64MB |

**`uploads`:** `false` turns uploading off (a build without the `files`
feature has none to turn off). The Incoming page takes no drops,
and `/api/uploads` answers 404.

**`upload_preallocate`:** `true` reserves an upload's whole size on the drive
before the first piece arrives, so a full drive is found at the start
rather than part way. Worth it on drives that can do this quickly (ext4,
APFS). On exFAT it means writing the whole size in zeros first, so leave it
off there.

**`upload_buffer`:** each upload is written to the drive through a buffer of
this size, never held in memory or `/tmp`. Bigger buffers mean fewer, larger
writes, but each upload under way has one: two at once at 64MB would hurt a
router. 256KB is fine.

### Bonjour

The server announces itself on the local network (mDNS/DNS-SD, as
`_mvd._tcp`), so the Apple TV app lists it at setup.

| Setting | Default | |
|---|---|---|
| `mdns` | `true` | announce the server on the local network |
| `mdns_name` | the machine's name | what the apps show |
| `mdns_interfaces` | `""` (every private network) | only on these interfaces, comma-separated |

**`mdns_name`:** what the Apple TV app lists it as: `"Living room"`, say.
If another server on the network has the name already, this one adds a
number while it runs: `"Living room (2)"`.

**`mdns_interfaces`:** it only ever answers on private networks, never a
public address. On a router whose WAN side is a private network too (behind
another router), name the LAN to keep it off the WAN:
`mdns_interfaces = "br-lan"` on GL.iNet routers. `ip addr` lists the
interfaces.

It answers over IPv6 as well as IPv4, giving the interface's link-local
and unique local (`fd..`) addresses, unless `address` is an IPv4 one
(`"0.0.0.0"` included): then the server can't be reached over IPv6, and
no IPv6 address is given.

## `[tmdb]`

Where mvd gets titles, plots, artwork and episode lists.

| Setting | Default | |
|---|---|---|
| `api_key` | (none) | your TMDB key |
| `language` | `"en-US"` | the language of titles and plots |
| `region` | `"GB"` | whose age ratings to use |

**`api_key`:** needed for `mvd import` and `mvd refresh`. Either a v3 API key
or a v4 read access token works. See
[Getting a TMDB key](../README.md#getting-a-tmdb-key). `TMDB_API_KEY` in the
environment overrides it.

**`language`:** a language and region, like `en-US`, `en-GB`, `fr-FR` or
`de-DE`. Changing it affects what's fetched from then on; `mvd refresh -all`
fetches everything again in the new language. `mvd refresh -lang` overrides
it for one run.

**`region`:** a country code, for the age rating shown and written in the
NFOs: `GB` for BBFC (12A, 15), `US` for the MPA (PG-13, R), and so on.
`mvd refresh -region` overrides it for one run.

## `[downloads]`

Downloads, with the `downloads` feature built in. Its settings are
explained in [DOWNLOADS.md](DOWNLOADS.md#settings).

## `[client]`

Settings for the client commands (`mvd upload`, `mvd progress`,
`mvd status`), on a machine other than the server.

| Setting | Default | |
|---|---|---|
| `server` | this machine's, else the one found on the network | where the client commands find the server |

**`server`:** the server's address, like `"http://router:6837"`. `mvd upload
-server` overrides it. Set `[server] token` to the server's token too, if it
has one (or pass `-token`). On the server's own machine, leave it unset:
the commands follow `[server] address` and `port` (`127.0.0.1` when the
server listens on every interface). Set, it wins over both.

On another machine, it can be left unset too if there's one server on the
local network: the commands look for it (by Bonjour, taking a second each
time) when nothing's listening on this machine. With several servers, or
on a network that doesn't pass multicast, set it.

## Environment

| Variable | |
|---|---|
| `TMDB_API_KEY` | overrides `[tmdb] api_key` |
| `XDG_CONFIG_HOME` | where to find `mvd/config.toml`, instead of `~/.config` |
| `HOME` | `~` in paths, and the default config and data folders |
| `GOMEMLIMIT` | Go's soft memory limit: the procd script sets `15MiB`, so mvd collects garbage harder before crowding out anything else on the router |
| `GODEBUG=gctrace=1` | logs each garbage collection, for watching memory |
