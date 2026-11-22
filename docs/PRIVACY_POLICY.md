# Privacy policy

Last updated November 13, 2026.

mvd is a media server you run yourself, with apps that play from it. This
policy covers the mvd apps (Apple TV, iPhone and iPad) and the mvd server
and its website.

## The short version

mvd collects nothing about you. There are no accounts, no analytics, no
advertising and no tracking. The apps talk only to your own mvd server, and
nothing is sent to the people who make mvd.

## What the apps keep

- **Your server's address,** kept on the device so the app can connect
  again. On Apple TV it's shared with the app's Top Shelf, which shows your
  Up Next on the home screen.
- **A key for your server,** if it has a token, kept in the device's
  Keychain. It's made from the token, or given by the server when you
  pair the device; the token itself isn't kept. It isn't included in
  backups and doesn't move to a new device.
- **Your choices in the app,** such as how the library is sorted.

The address and your choices are in the device's backups, if you make
them. "Use a Different Server", in Settings, forgets the address and the
key, and the pictures the app had saved from that server. Deleting the
app deletes the address and your choices; on iPhone and iPad the system
may keep the key in the Keychain afterwards, where nothing can use it
without the address.

## What the apps send, and to whom

Only to your mvd server, at the address you gave it:

- requests for your library, artwork and subtitles, and the videos you play;
- how far you've got in what you watch, so you can resume and see what's up
  next on any device;
- what you mark as watched, or take out of Up Next.

Your server keeps that watch history in a file of its own, one for each
token (`state.json`, `state-kids.json`, in its data folder), on your
hardware. It isn't sent anywhere else. Clearing a
title's progress, or marking it unwatched, removes it; deleting that file
removes the lot.

The apps don't send your server's token. Each request is signed with
the key above, so a device that answers at your server's address
without being your server learns nothing it can use.

The apps ask to use your local network, to find mvd servers on it. They
use that to find and talk to mvd servers, nothing else.

## The server

The mvd server runs on your own computer or router. It contacts:

- **The Movie Database (TMDB),** to look up the titles of your movies and
  shows and download their artwork and descriptions. TMDB sees the titles it's
  asked about (including what you search for on the website's Incoming page)
  and your server's internet address, as with any website. Pictures from TMDB
  that the website shows come through your server, so TMDB never sees your
  browser. See [TMDB's privacy policy](https://www.themoviedb.org/privacy-policy).

Nothing else. The website the server shows is served by your server alone,
with nothing loaded from anywhere else.

## Children

mvd collects no information from anyone, children included.

## Changes

If this policy changes, the new version will be here, with its date at the
top.

## Contact

Questions about this policy: email contact@potato.video.
