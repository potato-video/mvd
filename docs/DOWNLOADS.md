# Downloads with Transmission

This needs the `downloads` feature, which the standard build has (the
release builds don't). The settings are [below](#settings).

mvd adds and tracks downloads through Transmission, running on the same
machine. Transmission puts finished downloads in the import folder and
partial ones in a hidden `.downloading/` inside it. mvd lets each finished
download seed to `seed_ratio`, then takes it out of Transmission (keeping
the files) and imports it.

## Setting it up

The setup guides for [OpenWRT](SETUP_OPENWRT.md#downloads-with-transmission)
and [Alpine Linux](SETUP_ALPINE.md#downloads-with-transmission) walk through
this for each machine. In short:

1. **Configure mvd first**, since the setup is filled in from your config:

   ```toml
   [downloads]
   transmission  = "http://127.0.0.1:9091/transmission/rpc"
   seed_ratio    = 1.0    # 0: import as soon as it's downloaded
   max_downloads = 2      # at most this many at once, the rest queued
   ```

2. **Print the setup**, on the machine mvd runs on:

   ```bash
   mvd setup transmission
   ```

   This prints the commands to install `transmission-daemon` and point it at
   mvd's folders. It also keeps Transmission's own state on the drive beside
   the library, and sizes its cache and peer limits for the machine's
   memory. Read the commands, then run them as root. There are two kinds,
   picked for the machine it runs on, or named (`mvd setup transmission
   alpine`):

   - **OpenWRT** (`openwrt`) sets Transmission up through `uci`. It works
     around the problems found on GL.iNet's firmware (see
     [Problems](#transmission-keeps-crashing-or-wont-start)), and keeps
     the state off `/tmp`, which is RAM there.
   - **Alpine Linux** (`alpine`) runs Transmission as the user mvd runs as,
     `mvd`, in `/etc/conf.d/transmission-daemon`, so what it downloads is
     mvd's to move and trash. It writes Transmission's `settings.json`
     while it's stopped, keeping any old one as `settings.json.old`.
     Transmission logs through syslog.

3. **Optionally, open the peer port.** On a router, Transmission runs on the
   firewall itself, so UPnP doesn't help. Incoming peers need a firewall
   rule for TCP and UDP 51413 to the router, which the OpenWRT setup
   prints, commented out. On a machine behind a router (a Pi), Transmission
   asks the router for the port by UPnP or NAT-PMP, or you forward it
   yourself. It works without either, just with fewer peers.

Transmission's RPC is bound to `127.0.0.1` by the setup, so only mvd talks
to it. If you also use Transmission's own web UI from the LAN, turn on its
authentication and give mvd `username` and `password`.

## Settings

Under `[downloads]` in config.toml. An older config's `[torrent]` is read
as `[downloads]`.

| Setting | Default | |
|---|---|---|
| `transmission` | (none) | Transmission's RPC address |
| `username` | `""` | if Transmission wants a login |
| `password` | `""` | |
| `seed_ratio` | `1.0` | seed to this ratio before importing |
| `max_downloads` | `2` | at most this many downloading at once |

**`transmission`:** usually `"http://127.0.0.1:9091/transmission/rpc"`, for
Transmission on the same machine. Without it, downloads are off, and imports
don't check for torrents still seeding. mvd and Transmission must see the
library at the same paths.

**`username` and `password`:** only if Transmission's RPC authentication is
on (`rpc_authentication_required`). `mvd setup transmission` binds the RPC to
`127.0.0.1` instead, so only this machine can reach it.

**`seed_ratio`:** a finished download seeds until it has uploaded this many
times its size, then mvd takes it out of Transmission (keeping the files)
and imports it. `0` imports as soon as it's downloaded. Transmission's own
seeding limits, if you set them, also count: a torrent Transmission has
stopped is ready. mvd sets each torrent it adds to this ratio, so
Transmission stops seeding there too.

**`max_downloads`:** this goes into Transmission's download queue when you
run `mvd setup transmission`, so set it first. The rest wait, queued. On a
router, a couple at once finish sooner than many crawling along together.
`0` leaves Transmission's own setting.

## `mvd torrent`

```
mvd torrent add [-movie|-tv] SOURCE...      magnet links, .torrent URLs or files
mvd torrent list                            in queue order, with progress and peers
mvd torrent pause [ID|NAME...]              all, or those named
mvd torrent resume [ID|NAME...]
mvd torrent verify [ID|NAME...]             those in error, or those named
mvd torrent remove [-keep] [-y] ID|NAME...  cancel one, or stop seeding it
mvd torrent clear [-keep] [-y]              the same, for all of them
```

- A download is named by its ID (from `mvd torrent list`), its hash, or part
  of its name. IDs are Transmission's, given afresh each time it starts;
  the hash never changes.
- `-movie` or `-tv` downloads into `import/movies` or `import/tv`, for when
  the name doesn't make it obvious.
- **Removing** an unfinished download cancels it and deletes its partial data
  (unless `-keep`). Removing a finished one stops it seeding and keeps the
  files, so the next import takes them. Both ask first, unless `-y`.
- **Verifying** has Transmission check what a download has on disk, marking
  anything missing or damaged to download again, then starts it, which
  clears its error. With no names, it takes the ones Transmission reports an
  error for. See [A download shows a problem](#a-download-shows-a-problem)
  for what the errors mean.
- **Pausing** a download that's seeding short of `seed_ratio` holds its import
  until it's resumed and gets there, or is removed.
- **Releases in archives** (`.rar` sets) don't wait for the seeding: once
  downloaded, they're unpacked beside the archives and filed, since
  unpacking leaves the archives as Transmission expects them. The archives
  carry on seeding, and once it's done the download leaves Transmission
  and the archives go to the trash. A download with loose videos as well
  waits as usual, since filing those would stop it seeding.
- Downloads go one or two at a time (`max_downloads`), the rest queued. On a
  router, a couple at once finish sooner than many crawling along together.

The web UI's Incoming page does all of this too: see
[On the website](#on-the-website).

## On the website

The Incoming page ([CLIENTS.md](CLIENTS.md#incoming)) does what
`mvd torrent` does, with the `files` feature built in too:

- **Add:** paste a magnet link or a link to a `.torrent` into the field and
  press **Add**, or drop a `.torrent` file anywhere on the page.
- **The list** shows Transmission's queue, then downloads seeding, between
  the uploads and what's waiting for an import. Their rows say "via
  Transmission".
  - Drag a download up or down to change the order they download in (by its
    handle, on a phone).
  - **i** shows a download's seeds, leechers and ratio, then when it was
    added, its speed (down while downloading, up while seeding) and its
    content ("762 MB in 6 files": Transmission 4 and later say how many).
    "Seeds 14 of 312" means 14 connected and sending, of the 312 the trackers
    last counted.
  - **Pause** and **Resume** a download, or all of them with **Pause all**
    and **Resume all**.
  - **✕** cancels an unfinished download and throws away its partial data.
    **🗑** on a finished one moves it to mvd's trash; one still seeding
    leaves Transmission. Both ask first.
- **Needs you** holds any download Transmission reports a problem with,
  with **Verify** (as `mvd torrent verify`: Transmission checks its data and
  starts it again) and **✕**. See
  [A download shows a problem](#a-download-shows-a-problem).
- **Problems with everything** (Transmission out of reach) show once,
  above the list.

## Problems

### Transmission keeps getting killed

On a 128 MB router with no swap, the kernel kills the biggest process when
memory runs out, and Transmission is usually the biggest. As well as
[what the FAQ says for mvd](FAQ.md#mvd-keeps-getting-killed), check that
Transmission was set up with `mvd setup transmission`, which sizes its
cache and peer limits to the router's memory.

### Transmission keeps crashing or won't start

These are the problems found on a GL-AR750S with GL.iNet's firmware. `mvd
setup transmission` handles all of them, but if you set Transmission up
yourself:

- **"jail: failed to load dependencies" or "symbol not found".**
  `transmission-daemon` needs `libmbedtls12`, which the firmware doesn't
  install. Install it.
- **It vanishes every few minutes, with nothing in the log.** OpenWRT's
  seccomp filter for Transmission leaves out `ftruncate64`, which
  Transmission 3.00 calls when it starts a new file, and the kernel kills it
  silently. Add `ftruncate` and `ftruncate64` to
  `/etc/seccomp/transmission-daemon.json`.
- **It can't write to the drive.** exFAT has no file ownership, so
  Transmission's own user can't write there. Run it as root.
- **Torrents vanish at reboot.** Its `config_dir` defaults to `/tmp`, which is
  RAM. Move it to the drive.
- **A torrent stops with "No data found!".** The incomplete folder must be
  inside `download_dir` (mvd uses `import/.downloading`), since the procd
  jail only mounts `download_dir`.
- **Settings you changed in its web UI are lost.** On OpenWRT, Transmission's
  settings come from UCI (`/etc/config/transmission`), and are written over
  `settings.json` at every start.

### A download shows a problem

When a download says "Problem: ...", that's Transmission's own error
message, passed on. Most clear with:

```
mvd torrent verify
```

which has Transmission check each failed download's data on disk, mark
anything damaged to download again, and start it. What the common ones mean:

- **"Please Verify Local Data! Piece #N is corrupt."** Transmission couldn't
  read back a piece it had already downloaded and stopped the torrent.
  That's nearly always the drive, not the download. Verify, then look at
  the kernel log:

  ```
  dmesg | grep -i -E "i/o error|reset|uas"
  ```

  `I/O error` lines, USB resets or `uas_eh_abort_handler` lines mean the
  drive's connection is dropping out. A high "Corrupt DL" in
  `transmission-remote -t ID -i` is another sign. On a router the usual
  causes, most likely first, are:
  - **Power:** a bus-powered 2.5" drive can draw more than the router's USB
    port gives. Use a powered USB hub, or a drive with its own power.
  - **The cable:** try a short, known-good one.
  - **UAS:** Linux's UAS driver doesn't get on with some USB enclosures
    (Seagate's often). Turn it off for the drive with a `usb-storage`
    quirk: find its ID with `cat /sys/bus/usb/devices/*/idVendor` and
    `idProduct` (say `0bc2` and `61b6`), then on OpenWRT add
    `options usb-storage quirks=0bc2:61b6:u` to a file in `/etc/modules.d/`
    and reboot.

  If the errors name the same sectors over and over, the disk itself may be
  failing: copy the library off it.
- **"No data found!"** Transmission can't find the files: the drive isn't
  mounted, or (on OpenWRT) the incomplete folder is outside `download_dir`.
  See [Transmission keeps crashing or won't
  start](#transmission-keeps-crashing-or-wont-start).
- **A tracker error** ("Tracker gave HTTP response code ...", "Could not
  connect to tracker"). The tracker's down or gone. The download carries on
  with its other trackers and peers, if it has any, and verifying won't
  help. If it never gets going, remove it and find another source.
