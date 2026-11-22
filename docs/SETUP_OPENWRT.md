# Setting mvd up on an OpenWRT router

This is the reference setup: a GL-AR750S running GL.iNet's firmware (OpenWRT
22.03), with a USB drive mounted at `/mnt/drive`. The router is at
`192.168.8.1`, the default address on GL.iNet routers. Change the paths and
the address to suit your own. Everything runs as root, as is usual on
OpenWRT.

- [Build](#build)
- [Mount the drive](#mount-the-drive)
- [Install](#install)
- [Configure](#configure)
- [Run it as a service](#run-it-as-a-service)
- [Memory](#memory)
- [Import, and open it](#import-and-open-it)
- [Downloads with Transmission](#downloads-with-transmission)
- [Sharing the drive over SMB](#sharing-the-drive-over-smb)
- [Updating](#updating)
- [Where things are](#where-things-are)

## Build

The quick way is the installer, run on the router once
[the drive is mounted](#mount-the-drive): it fetches a release build and
does the Install, Configure, service and Memory steps below, asking where
the library is and for a TMDB key.

```bash
wget -O install.sh https://potato.video/install.sh && sh install.sh
```

The release builds have no Transmission downloads. For those, or to do it
by hand, build mvd on your computer, with Go 1.24 or later:

```bash
git clone https://github.com/potato-video/mvd.git && cd mvd
```

```bash
make router
```

This gives `mvd-linux-mips`, for big-endian MIPS with software floating
point (the AR750S, AR300M and similar). `ssh root@192.168.8.1 uname -m`
tells you what your router is. For others, set the target yourself: a
little-endian MIPS router (MT7621-based, say) is `GOARCH=mipsle`, and an
ARM one `GOARCH=arm` or `arm64`:

```bash
make router ROUTER='CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat' ROUTER_OUT=mvd-linux-mipsle
```

`make sizes` shows what each optional feature adds to the binary, if flash
or memory is tight (see [Optional parts](../README.md#optional-parts)).

## Mount the drive

OpenWRT mounts drives through `block-mount`, configured in
`/etc/config/fstab` (not `/etc/fstab`). GL.iNet's firmware mounts a USB
drive by itself when it's plugged in, but under a name that depends on the
drive. A fixed entry gives it the same path every time. With the drive
plugged in, on the router:

```bash
opkg update && opkg install block-mount kmod-fs-exfat
```

Use `kmod-fs-ext4` instead for ext4. Most firmware has both already, and
`opkg` says so. Then find the drive:

```bash
block info
```

Note its `UUID`. Use the UUID rather than `/dev/sda1`, since device names
can change. Add it:

```bash
uci add fstab mount && uci set fstab.@mount[-1].uuid='your-uuid' && uci set fstab.@mount[-1].target='/mnt/drive' && uci set fstab.@mount[-1].options='noatime' && uci set fstab.@mount[-1].enabled='1' && uci commit fstab
```

```bash
/etc/init.d/fstab enable && block mount && df -h /mnt/drive
```

**exFAT** is the usual choice here, so the drive also goes straight into a
Mac or PC. It has no file owners: everything belongs to root, which is
who mvd and Transmission run as on the router. **ext4** works too, and is
more robust if the router loses power mid-write, but a Mac can't open it.

If a drive isn't there at boot, the router starts anyway, and mvd's
service waits for the binary to appear on it.

## Install

Put the binary on the drive, not the router's flash, which is small:

```bash
ssh root@192.168.8.1 mkdir -p /mnt/drive/bin
```

```bash
scp -O mvd-linux-mips root@192.168.8.1:/mnt/drive/bin/mvd
```

`-O` makes scp use the old protocol, since OpenWRT's SSH server (Dropbear)
has no SFTP.

To type `mvd` rather than the whole path, link it in, on the router (the
link takes next to no flash):

```bash
ln -s /mnt/drive/bin/mvd /usr/bin/mvd
```

## Configure

On the router:

```bash
mkdir -p /root/.config/mvd && mvd setup config > /root/.config/mvd/config.toml && chmod 600 /root/.config/mvd/config.toml
```

Then edit it (`vi /root/.config/mvd/config.toml`). At least:

```toml
[paths]
video_dir = "/mnt/drive/video"   # holds movies/, tv/, import/ and .trash/
data_dir  = "/mnt/drive/.mvd"    # mvd's own files: on the drive, not the router's flash

[tmdb]
api_key = "your-key-here"
```

`data_dir` holds the library index, the TMDB cache and watch progress, a
few MB written to as people watch. Its default is under `/root`, on the
router's flash, which is small and wears out with writing (and `/tmp` is
RAM, lost at every reboot), so it goes on the drive.

A TMDB key is free: see
[Getting a TMDB key](../README.md#getting-a-tmdb-key). The server imports
what arrives by itself; to import only with `mvd import`, set
`auto_import = false` under `[library]`.
[CONFIG.md](CONFIG.md) explains every setting.

## Run it as a service

```bash
mvd setup service > /etc/init.d/mvd && chmod +x /etc/init.d/mvd
```

```bash
/etc/init.d/mvd enable && /etc/init.d/mvd start
```

The procd script waits for the drive to mount, restarts mvd if it stops,
and sets `GOMEMLIMIT=15MiB` so mvd leaves room for everything else. Add
`/etc/init.d/mvd` to `/etc/sysupgrade.conf` to keep it across firmware
upgrades.

## Memory

On a small machine with no swap like the AR750S, print and run the memory
script:

```bash
mvd setup memory > memory.sh && sh memory.sh
```

It turns off the kernel's watermark boosting, which otherwise raises the
free-memory reserve after fragmentation until processes are killed with
memory still free. See the [FAQ](FAQ.md#mvd-keeps-getting-killed)
if something still gets killed.

## Import, and open it

Put some videos in `/mnt/drive/video/import`, run `mvd import` on the
router, and open `http://192.168.8.1:6837/`. The Apple TV app finds the
server by itself (see [CLIENTS.md](CLIENTS.md)).

## Downloads with Transmission

This needs the `downloads` feature, which `make router` includes.

1. **Tell mvd where Transmission will be**, in `config.toml`, before the
   next step, since the setup is filled in from it:

   ```toml
   [downloads]
   transmission  = "http://127.0.0.1:9091/transmission/rpc"
   seed_ratio    = 1.0
   max_downloads = 2
   ```

2. **Print the setup, read it, then run it:**

   ```bash
   mvd setup transmission > transmission.sh && sh transmission.sh
   ```

   It installs `transmission-daemon` and sets it up through `uci`: its
   state on the drive (the default, `/tmp`, is RAM), downloads into
   `import/`, its cache and peers sized for the router's memory, and the
   workarounds GL.iNet's firmware needs. On an exFAT drive it runs as root,
   since exFAT has no file owners.

3. **Restart mvd** (`/etc/init.d/mvd restart`) so it picks up `[downloads]`.

Transmission runs on the firewall itself, so UPnP doesn't apply: for
incoming peers, the setup prints a firewall rule for port 51413, commented
out. [DOWNLOADS.md](DOWNLOADS.md) covers `mvd torrent` and the rest.

## Sharing the drive over SMB

To copy files on and off the drive from a Mac or PC, share it over SMB.
Anything copied into `video/import` is imported (by itself, with
auto-import, once it's been unchanged for a minute).

**Mind the memory.** Samba's `smbd` takes several MB, and more for each
connection. On a 128 MB router alongside mvd and Transmission, that's a
real share of what's left. The kernel's own SMB server, ksmbd, is much
lighter, and OpenWRT 22.03 and later have it:

```bash
opkg update && opkg install ksmbd-server luci-app-ksmbd
```

Then set the share up in LuCI (Services → Network Shares): the path
`/mnt/drive`, writable, with a user. Or, on GL.iNet's firmware, its own
admin panel has a network storage page that shares the drive over Samba.

On an exFAT drive, whatever the share writes is writable by mvd, since the
mount decides who can write and both run as root. Don't change
`.mvd/` or `.transmission/` over the share while they're running.

## Updating

Build again, copy the new binary over the old one, and restart:

```bash
scp -O mvd-linux-mips root@192.168.8.1:/mnt/drive/bin/mvd
```

```bash
/etc/init.d/mvd restart
```

`mvd version` tells you whether the running server is the binary you just
installed, or an older one still waiting for a restart.

## Where things are

| | |
|---|---|
| the drive's mount | `/etc/config/fstab` (`block info`, `block mount`) |
| the binary | `/mnt/drive/bin/mvd` |
| the config | `/root/.config/mvd/config.toml` |
| mvd's own files | `data_dir`, `/mnt/drive/.mvd` |
| the service | `/etc/init.d/mvd` |
| mvd's log | `logread -e mvd` |
| Transmission's log | `logread -e transmission` |
| Transmission's state | `/mnt/drive/.transmission` |
