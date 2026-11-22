# Setting mvd up on Alpine Linux

This is for Alpine on a Raspberry Pi, at `192.168.8.1`, with the drive
formatted ext4 and mounted at `/mnt/media`. Anything else running Alpine
is much the same. mvd runs as its own user, `mvd`, which owns the drive;
Transmission and the SMB share act as `mvd` too, so whatever arrives,
mvd can file it. Change the paths and the address to suit your own.

- [Before you start](#before-you-start)
- [Build](#build)
- [Prepare the Pi](#prepare-the-pi)
- [Mount the drive](#mount-the-drive)
- [Install](#install)
- [Configure](#configure)
- [Run it as a service](#run-it-as-a-service)
- [Import, and open it](#import-and-open-it)
- [Downloads with Transmission](#downloads-with-transmission)
- [Sharing the drive over SMB](#sharing-the-drive-over-smb)
- [Updating](#updating)
- [Where things are](#where-things-are)

## Before you start

```bash
ssh root@192.168.8.1 'uname -m; mount | grep " / "'
```

- `aarch64` needs a 64-bit build, `armv7` a 32-bit one.
- If `/` is `tmpfs`, Alpine is running **diskless** (the Pi image's
  default): the system lives in RAM, and changes are lost at reboot unless
  saved with `lbu commit`. This guide says where. Or install it to the SD
  card with `setup-disk -m sys`, and skip those steps.

## Build

The quick way is the installer, run on the Pi as root once
[the drive is mounted](#mount-the-drive): it fetches a release build and
does the Install, Configure and service steps below, asking where the
library is and for a TMDB key.

```bash
wget -O install.sh https://potato.video/install.sh && sh install.sh
```

The release builds have no Transmission downloads. For those, or to do it
by hand, build mvd yourself:

On your computer, with Go 1.24 or later:

```bash
git clone https://github.com/potato-video/mvd.git && cd mvd
```

```bash
make router ROUTER='CGO_ENABLED=0 GOOS=linux GOARCH=arm64' ROUTER_OUT=mvd-linux-arm64
```

For 32-bit, use `GOARCH=arm GOARM=7` and `ROUTER_OUT=mvd-linux-armv7`.

## Prepare the Pi

As root on the Pi. `ca-certificates` is for TMDB's HTTPS. The Pi has no
clock battery, so check `date` is right too: if it isn't, run `setup-ntp`,
or TMDB's certificates look invalid.

```bash
apk add ca-certificates e2fsprogs && adduser -D mvd
```

`e2fsprogs` formats and checks ext4 drives.

## Mount the drive

Alpine mounts what's in `/etc/fstab` at start-up (the `localmount`
service), so the drive gets a line there. Find it first, with the drive
plugged in:

```bash
blkid
```

It's usually `/dev/sda1`. Note its `UUID` and `TYPE`. Use the UUID rather
than `/dev/sda1` in fstab, since device names can change when another
drive is plugged in.

A new drive can be formatted ext4 first. This erases it, so check the
device name:

```bash
mkfs.ext4 -L media /dev/sda1
```

**ext4** is the best choice on Linux: it has file owners, so mvd can own
the library. Add the drive, then mount everything in fstab to check the
line works:

```bash
mkdir -p /mnt/media && echo 'UUID=your-uuid /mnt/media ext4 defaults,noatime,nofail 0 2' >> /etc/fstab && mount -a
```

- `noatime` saves a write each time a file is read.
- `nofail` lets the Pi start without the drive, rather than stopping at
  boot. mvd's service waits a minute for it, then fails, and starts when
  you restart it.
- `0 2` checks the filesystem at boot, after the root one.

Then make it mvd's:

```bash
mkdir -p /mnt/media/bin /mnt/media/video && chown -R mvd:mvd /mnt/media
```

**exFAT** (a drive that also goes into a Mac or PC) has no file owners, so
the mount decides who owns everything. Mount it as mvd: `id mvd` gives its
uid and gid (1000 for the first user added). `exfatprogs` is only for
checking and formatting; the kernel does the mounting.

```bash
apk add exfatprogs && mkdir -p /mnt/media && echo 'UUID=your-uuid /mnt/media exfat defaults,noatime,nofail,uid=1000,gid=1000,umask=002 0 0' >> /etc/fstab && mount -a
```

`chown` does nothing on exFAT, and isn't needed. `0 0` skips the boot-time
check, which Alpine can't do for exFAT.

Check it's mounted (`df -h /mnt/media`), and on a diskless install save
fstab:

```bash
lbu commit
```

## Install

From your computer:

```bash
scp -O mvd-linux-arm64 root@192.168.8.1:/mnt/media/bin/mvd
```

`-O` makes scp use the old protocol, since Alpine has no SFTP server unless
you add one (`apk add openssh-sftp-server`, with OpenSSH; Dropbear has
none).

On the Pi, link it into the PATH:

```bash
ln -s /mnt/media/bin/mvd /usr/local/bin/mvd
```

## Configure

As mvd, so the config is in `/home/mvd/.config/mvd/`:

```bash
su mvd -c 'mkdir -p ~/.config/mvd && mvd setup config > ~/.config/mvd/config.toml && chmod 600 ~/.config/mvd/config.toml'
```

Then edit it. At least:

```toml
[paths]
video_dir = "/mnt/media/video"   # holds movies/, tv/, import/ and .trash/
data_dir  = "/mnt/media/.mvd"    # mvd's own files

[tmdb]
api_key = "your-key-here"
```

`data_dir` holds the library index, the TMDB cache and watch progress, a
few MB written to as people watch. It goes on the drive rather than in the
usual `/var/lib`: on a diskless install `/var` is RAM, and `lbu commit`
saves only `/etc`, so watch progress would be lost at every reboot. On a
sys install, the drive still spares the SD card, which wears out with
writing and is the likelier of the two to fail.

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
rc-update add mvd default && rc-service mvd start
```

The OpenRC script runs mvd as `mvd`, waits for the drive, and restarts mvd
if it stops. A Pi has the memory a router lacks, so `mvd setup memory`
and `GOMEMLIMIT` aren't needed. If it's diskless, save it all now:

```bash
lbu commit
```

## Import, and open it

Put some videos in `/mnt/media/video/import`, then:

```bash
su mvd -c 'mvd import'
```

Run mvd's commands as `mvd` like this, so what they write belongs to it:
run as root, they leave files the server can't move or trash. Then open
`http://192.168.8.1:6837/`. The Apple TV app finds the server by itself
(see [CLIENTS.md](CLIENTS.md)).

## Downloads with Transmission

This needs the `downloads` feature, which `make router` includes.

1. **Tell mvd where Transmission will be**, in mvd's `config.toml`, before
   the next step, since the setup is filled in from it:

   ```toml
   [downloads]
   transmission  = "http://127.0.0.1:9091/transmission/rpc"
   seed_ratio    = 1.0
   max_downloads = 2
   ```

2. **Print the setup, read it, then run it** as root:

   ```bash
   mvd setup transmission > transmission.sh && sh transmission.sh
   ```

   It installs `transmission-daemon`, runs it as `mvd` (in
   `/etc/conf.d/transmission-daemon`), keeps its state in
   `/mnt/media/.transmission`, and writes its `settings.json`: downloads
   into `import/`, its RPC on `127.0.0.1` only, the queue, and its cache
   and peers sized for the Pi's memory.

3. **Restart mvd** (`rc-service mvd restart`) so it picks up `[downloads]`,
   and `lbu commit` if it's diskless.

For incoming peers, Transmission asks your router to forward port 51413 by
UPnP or NAT-PMP; if the router doesn't allow that, forward TCP and UDP
51413 to the Pi yourself. It works without, with fewer peers.
[DOWNLOADS.md](DOWNLOADS.md) covers `mvd torrent` and the rest.

## Sharing the drive over SMB

To copy files on and off the drive from a Mac or PC, share the whole drive
over SMB, as `mvd`. Anything copied into `video/import` is imported (by
itself, with auto-import, once it's been unchanged for a minute), and
the rest of the drive (music, say) is yours to use as you like.

```bash
apk add samba
```

This replaces the stock config:

```bash
cat > /etc/samba/smb.conf <<'EOF'
[global]
   server string = mvd
   server role = standalone server
   workgroup = WORKGROUP
   map to guest = never
   # Finder's metadata in extended attributes, not ._ files
   vfs objects = fruit streams_xattr
   fruit:metadata = stream
   fruit:model = MacSamba
   fruit:delete_empty_adfiles = yes
   fruit:wipe_intentionally_left_blank_rfork = yes

[media]
   path = /mnt/media
   read only = no
   valid users = mvd
   # everything written here is mvd's, so the server can file it
   force user = mvd
   force group = mvd
   create mask = 0664
   directory mask = 0775
   # Transmission's state isn't to be changed while it runs
   veto files = /.DS_Store/.transmission/
   delete veto files = yes
EOF
```

Check it, give `mvd` a Samba password (its own, not a login password), and
start it:

```bash
testparm -s && smbpasswd -a mvd
```

```bash
rc-update add samba default && rc-service samba start
```

If it's diskless, save the password database too, which isn't under
`/etc`:

```bash
lbu include /var/lib/samba && lbu commit
```

Connect to `smb://192.168.8.1/media` from a Mac (Go → Connect to Server),
or `\\192.168.8.1\media` from Windows, as `mvd`. It doesn't show up in the
Finder's sidebar by itself, since Samba doesn't announce itself over
Bonjour.

Some things to know:

- **Files are world-readable** (`0664`), so other services, such as mpd
  running as its own user, can read what's on the drive.
- **`.mvd/`** (mvd's own files) is shared but hidden by the Finder. Don't
  change it while the server runs. It isn't vetoed, since that would also
  block the `.mvd` hint files you can put in import folders.
- **A binary copied over the share isn't executable** (`0664`): update mvd
  with `scp`, as below.
- **After moving or renaming things** in `video/movies` or `video/tv`, run
  `su mvd -c 'mvd refresh'`.
- Keep the share to your own network: don't forward port 445.

## Updating

Run the installer again, if that's how mvd got there. Otherwise, build again, copy the new binary over the old one, and restart:

```bash
scp -O mvd-linux-arm64 root@192.168.8.1:/mnt/media/bin/mvd
```

```bash
rc-service mvd restart
```

`mvd version` tells you whether the running server is the binary you just
installed, or an older one still waiting for a restart. For Alpine itself,
`apk upgrade`, then `lbu commit` if it's diskless.

## Where things are

| | |
|---|---|
| the drive's mount | `/etc/fstab` |
| the binary | `/mnt/media/bin/mvd` |
| the config | `/home/mvd/.config/mvd/config.toml` |
| mvd's own files | `data_dir`, `/mnt/media/.mvd` |
| the service | `/etc/init.d/mvd` |
| mvd's and Transmission's logs | `/var/log/messages` (`grep mvd`, `grep transmission`) |
| Transmission's settings | `/etc/conf.d/transmission-daemon`, `/mnt/media/.transmission/settings.json` |
| Samba's settings | `/etc/samba/smb.conf` |
