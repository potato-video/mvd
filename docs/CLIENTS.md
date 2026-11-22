# Clients

Ways to watch what's on your mvd server. They all share one library and one
watch history: start something on the Apple TV, and the website knows where
you got to.

- [Web](#web): built into the server.
- [Apple TV](#apple-tv): a native app.
- [iPhone and iPad](#iphone-and-ipad): the same app, by touch.
- [Terminal](#terminal): planned.
- [Any other player](#any-other-player): VLC, mpv, IINA and the like, by URL.

For installing and running the server itself, see the
[README](../README.md).

## Web

If the server was built with the `webui` feature (the standard build is),
open `http://<server>:6837/` in a browser. With a `token` set, it asks for
the password (a token) once and remembers it (in a cookie made from the
token, not the token itself). The foot of each page says which token the
browser has, with "Sign out". A [viewer's token](CONFIG.md#tokens) sees
the library and its own Up Next, without Incoming. **Pair**, at the foot
of the page, lets an Apple TV in without the password being typed on it:
the TV shows a code, and you type it there (or open the link its square
code holds), check it, and let it in. The TV gets the same access as the
browser; signed in as admin, you choose which token it gets. `mvd pair`
does the same from a terminal.

The tabs along the top are **Up Next**, **Movies**, **TV** and **Incoming**,
with **Search** (the magnifying glass) at the right.

### Up Next

One list of what to watch, at most one entry per movie or show:

- **Continue watching:** movies and episodes you're part-way through.
- **Next episode:** for shows you've started, the episode after the one you
  watched most recently (so rewatching episode 1 offers episode 2).
- **New:** movies added in the last 30 days that you haven't watched, and shows
  with new episodes (the one to watch next, with how many are waiting).

The newest is first, by when you last played it or when it arrived.
Part-watched things get a head start.

The selected entry fills the screen beside the list, with Play (or Resume),
Remove from Up Next, and its `.m3u` link. Click a row, or tab to it, to show
it there; click it again, or press Enter, to play it. The ↑ and ↓ keys move
along the list.

- **Clearing progress:** hover over the bottom of a thumbnail, or over the
  progress line above the buttons.
- **Remove from Up Next** hides an entry without touching its progress. A
  movie comes back when you play it, a show when you play any of it or new
  episodes arrive.

On a phone, the featured entry sits above the list.

### Movies and TV

Poster grids, sorted with the select at the top right: Alphabetically
(the default), Year, Date Added, Last Watched, then Runtime (movies) or
Unwatched Episodes (shows), as in the Apple apps. The arrow beside it
reverses the order. Each grid remembers its order in this browser. Each card shows a movie's year and length (or
how much is left), or for a show, "new" / "3 new" or "watched" / "all
watched".

A movie's page has its poster and title over the fanart, the facts and plot,
then Play (or Resume) and Mark as Watched, and the `.m3u` link. A movie in
several parts has one Play button: the player goes from one part to the
next.

A show's page has the same at the top, with "Play S01E04" (or "Resume
S01E04") for the next episode. Below are its seasons as tabs, over an
episode list where each episode has its own Play and watched buttons.

### Search

The magnifying glass at the top right. Type a few letters and the results
follow as you type: **Movies**, **TV** and **Episodes**, best matches first,
at most 40 of each.

- **What's searched:** titles, a movie's collection ("godfather" finds the
  whole collection), directors, and the year ("alien 1979"). Not genres,
  plots or cast.
- **A few letters of each word will do,** in any order: "god fa" finds
  "The Godfather Part II". Words have to match from their start, so
  "father" doesn't.
- **Case, accents and punctuation don't count:** "amelie" finds "Amélie".
- **A show's name** finds the show, then its episodes.
- **An episode opens its show's page at that episode,** on its season, with
  its row marked.

The address (`/search?q=…`) can be bookmarked or shared. Without
JavaScript, type and press Enter. The Apple TV app and `mvd search` find
the same things: the search is the server's.

### Playing

The player page resumes where you left off and records your progress as you
watch. Browsers can only play some files: MP4 with H.264 reliably, and MKV in
Chrome. mvd doesn't transcode, so for everything else use the `.m3u` link
on the movie or episode page (see [Any other player](#any-other-player)).
Subtitles beside the video (`.srt` files) are offered in the player.

### Incoming

(With the `files` feature, as in the standard build.) Getting things into
the library from the browser: everything on its way in,
in one list, whatever brought it, and what needs you. Once something is in
the library, it's gone from this page. The figures beside the title are the
free space on the drive and how much is in mvd's trash; click the trash's to
empty it now (it asks first), rather than waiting for `trash_max_age`. Only
what mvd put in the trash is deleted.

- **Add:** drop files and folders anywhere on the page ("choose files" and
  "a folder" do the same from a picker). With downloads set up, the page
  takes those too: see [DOWNLOADS.md](DOWNLOADS.md#on-the-website).
  Videos, subtitles, `.nfo` files, and `.zip` or `.rar` archives (which the
  import unpacks) are uploaded into the import folder. There's no movie or
  TV switch: the import works it out, and asks in Needs you when it can't.
  - Uploads go in 16 MB pieces, each written to the drive before the next. A
    dropped connection, or a laptop asleep for a while, costs a piece, not
    the file: the page waits and carries on.
  - If the tab closes part way, the upload shows as stopped: drop the same
    file again and it picks up where it got to. Unfinished uploads left
    untouched for a week are cleared away.
  - **Folders** keep their layout (a season with its `Subs/` folder, say).
    Only videos, subtitles, `.nfo` files and archives are sent (no
    `.DS_Store` or sample `.txt` files). A folder only appears in the import
    folder once every file is in, so an import never takes half a season.
- **The list,** top to bottom: uploads from elsewhere (another browser, or
  `mvd upload`), this browser's uploads, any downloads, then what's waiting
  for an import. Each row says how much is in and where it's from ("1.2 GB
  of 4.1 GB from this computer") and how long to go.
  - Uploads don't pause: close the tab and drop the files again later.
  - **✕** stops something unfinished and throws away what's arrived (an
    upload's pieces, say). **🗑** moves something finished to mvd's trash,
    where it stays until `trash_max_age`. Both ask first.
  - With `auto_import` on (the default), what's waiting is imported within a
    minute or so. With it off, **Import now** sits above the list.
- **Needs you,** always beside the list (on a phone, above it, and only
  when something's there), holds anything the import can't take further
  without a person, each with a gold edge. The nav shows how many beside
  Incoming.
  - **Couldn't tell which this is:** the titles it might be, as `mvd import`
    asks at a terminal, each with how well it matched, and the arrow to its
    page on TMDB. Choosing one opens a dialog with its poster, facts and
    plot, and what will be imported ("Into Movies", or "8 episodes into TV,
    S01E01 to S01E08"). **Import** settles it (the item's `.mvd` hint) and
    starts an import; **Cancel** or Escape goes back.
  - **The search field** under the titles takes words ("Dune", or "Dune
    (1984)") to search TMDB, or a TMDB or IMDb id or link, which opens the
    dialog for that title straight away. When the import couldn't tell
    whether it's a movie or TV, the card asks first.
  - **Other problems** say what's wrong: already in the library, no videos
    in it, archives mvd can't unpack, videos that weren't filed. **🗑**
    moves it to the trash. Something that failed three imports in a row
    (a file it couldn't move, say) has **Try again**.
  - An item with more than one movie, or movies and episodes mixed, can't
    be settled by one answer: run `mvd import` at a terminal, or split it.
  - Answered at a terminal meanwhile? The card goes at the next refresh;
    an Import from a page that's out of date is refused, and says why.
  - There's no Skip: leave the card, and the item isn't tried again until
    it changes.
- **Problems with everything** (an import that
  stopped because TMDB couldn't be reached or refused the key) show once,
  above the list.

Uploads can be turned off with `uploads = false` under `[server]`.

## Apple TV

A native app for Apple TV (tvOS 18 or later): your library on the big
screen, with the remote. It plays everything in the library, MKV included,
through VLC's player built into the app.

[App Store]()

The app is for watching. Downloads and uploads stay on the website. If you'd
rather build it yourself, it's
[mvd-apple](https://github.com/potato-video/mvd-apple), and building it is in its
DEVELOPMENT.md.

With Reduce Motion on (Settings → Accessibility → Motion), the app's own
buttons don't grow when focused (their color shows it), pictures crossfade more quickly,
lists and panels don't slide, and long titles don't scroll.

### Connecting

The first time it opens, the app looks for mvd servers on your network and
lists what it finds. Pick yours.

If it has a token, the app asks for the password: type it, and it connects.

Or sign in with your phone: point its camera at the square code beside
the password field and open the link. It's the server's website. A phone
that's already signed in there shows one button, to let this Apple TV in;
one that isn't asks for the password first, on the phone's keyboard. The
TV connects a moment later, with the same access as the phone. Without a
phone, go to the address under the square code on any computer and type
the code shown with it; or run `mvd pair` on the server. A server built
without the website has no square code: the TV shows the `mvd pair`
command to run on the server.

To set up a child's Apple TV, type their password (their
[token](CONFIG.md#tokens)), or let it in from a phone signed in with it.
From a browser signed in as admin, you choose which token the TV gets. It
then sees only what that token can, with its own Up Next.

If the server's token changes, the app asks you to sign in again.

A server that isn't found (on another network, or where multicast doesn't get
through) can be typed in below: its address, and the port (6837 unless
you've changed it). Just the address: mvd can't be at a path after it
(behind a reverse proxy at `nas/mvd`, say), and the app says so.

A server picked from the list is remembered by its name as well as its
address. If your router later gives it a different address, the app finds
it again by name and carries on, the Top Shelf too; Settings shows the new
address. A server typed in is only looked for at the address you typed.

**Settings**, the last tab, is laid out like the Apple TV's own: a list on
the right, and more about what's focused on the left. Under Server are the
address and port of the server you're connected to, then "Use a Different
Server" (which asks first: it forgets the token too). Next to them are the
app's version and the server's, how long the server's been up, how many
movies, shows and episodes there are, the library's size and the free space
on its drive. While the server isn't answering, that's just the app's
version, then "Can't reach the server" (as over the other tabs) and why,
and the address and port are marked. Under Legal are the
privacy policy
([PRIVACY_POLICY.md](PRIVACY_POLICY.md): it collects nothing), mvd's license
(the MIT License) and the acknowledgements; under Contact, where to get
support and the email address.

### Up Next

The first tab is the same Up Next as on the website: the same list, in the
same order. The list runs down the left. The focused entry fills the rest of
the screen with its backdrop, title, facts and plot, and its buttons:

- **Play** (or **Resume**, carrying on where you left off) and **Remove from
  Up Next**. Part-way through something, a progress line above them says how
  much is left.
- Clicking a row plays it. Moving right from the list takes you to the
  buttons.
- Long-press a row for **Remove from Up Next**, and **Clear Progress** if
  you're part-way through it.

### Movies and TV

Poster grids, each card with its year or what's left to watch. A line
above them says the order they're in ("Sorted by date added"). The sort
drawer's edge shows down the left of the screen: move left off the first
column to open it, then pick Alphabetically, Year, Date Added, Last
Watched, then Runtime (movies) or Unwatched Episodes (shows).
Alphabetically runs A to Z, Runtime shortest first, the rest newest or most
first; select the current sort again to reverse it. Move right or press Menu to close the
drawer. Each tab remembers its order. Clicking a poster opens its page:

- **A movie's page:** full-screen fanart, with the poster, title, facts and
  plot, then Play (or Resume) and Mark as Watched.
- **A show's page:** "Play S02E03" (or "Resume S02E03") for the next
  episode, then the seasons as tabs over the episode list. Long-press an
  episode to mark it watched or unwatched.

Menu goes back from a movie's or show's page to the grid, from the tab bar
too.

### Search

The magnifying glass at the left of the tab bar. Type with the remote or
the keyboard on your phone, or hold the microphone button to dictate; the
results follow as you type, under **Movies**, **TV** and **Episodes**. The
scope bar under the field narrows them to one of those, or **All**.
It finds what [the website's search](#search) does, the same way: a few
letters of each word, in any order, from titles, collections, directors and
years.

Clicking a movie or a show opens its page. Clicking an episode opens its
show's page at that episode: on its season, with the episode's still
focused, ready to play.

It searches inside mvd only: the Apple TV's own Search and Siri don't see
your library.

### Play buttons

Resume carries on without asking. To start from the beginning instead,
long-press Resume: a small panel opens with **Resume** and **Play from
Beginning**. Clicking an episode you're part-way through, in a show's
episode list, asks which you want; its long-press menu has Play from
Beginning too.

### The player

| On the remote | |
|---|---|
| Click the middle | play or pause |
| Click the left or right edge | back or forward 10 seconds |
| Swipe left or right while paused | scrub through the video |
| Swipe up or down | to the Subtitles and Audio menus (click to open one) |
| Menu | stop, back to where you were (from the menus: back to the video) |

The controls show the title, the time so far, the time left,
a small playing or paused icon, and the Subtitles and Audio buttons. They stay up while it's paused. While a
file opens, or the picture stops to wait for more of it, the controls come
up with a small spinner in place of the time left.

Subtitles come from the file itself, and from `.srt` files beside it on the
server. They start off, unless a track is forced or the file marks one as
its default. Forced tracks are listed first, after Off. Your progress is saved every 10 seconds and when you pause.

Leaving the app while something plays (the TV button, Control Center, the
TV turned off) pauses it and saves your progress. Coming back, it's paused
where you left it, with your audio and subtitles as they were: press Play
to carry on.

Control Center, the Remote in an iPhone's Control Center, and Siri know
what's playing, with its poster, and how far it's got, and can play, pause, skip 10 seconds
either way, and move through it.

At the end of a movie, it's marked watched and the player closes. At the end
of an episode, the next episode is offered: its still, title and plot, with
**Play Next Episode**. A movie in several parts plays straight through from
one part to the next. Where you got to is saved during the first part,
short of its last few minutes, so stopping later resumes there; the movie
is marked watched at the end of the last.

If playback stops part-way, most likely because the connection to the
server dropped, the player says so and offers **Try Again**, which carries on
from where it stopped, and **Exit**. While it tries, a spinner shows; if the
server still can't be reached, Try Again comes back to try once more. It
isn't marked watched, unless it was already in the credits (the last three
minutes, or the last 10%).

### Top Shelf

With mvd in the top row of the Apple TV's home screen, the Top Shelf shows
your Up Next. Select an entry to open it in the app, or press Play to start
it straight away.

## iPhone and iPad

The same app on an iPhone or iPad (iOS 18 or later): the same tabs, pages
and words as on the Apple TV, used by touch. It plays everything, MKV
included, through VLC, as the Apple TV does. It's handy on the road, with
the server on a travel router in the car, and in bed or another room at
home. One purchase covers the Apple TV, iPhone and iPad.

As on the Apple TV, it's for watching: no downloads, uploads or copies to
keep offline.

### Connecting

As on the Apple TV: the servers found on your network, or the address and
port typed in, and, when the server has a token, its password, typed or
pasted. The first time it looks,
iOS asks whether mvd may find devices on your local network. If that's
been refused, the setup screen says so, with a button to mvd's page in
Settings; a typed address works either way. As on the Apple TV, a server
picked from the list is found again by its name if its address changes.

**Settings**, the last tab, has the same as the Apple TV's, where Support
and Email open Safari and Mail. On an iPad, or an iPhone on its side, it's
laid out like the Apple TV's too; on an iPhone held upright it's one list,
with the versions, uptime and library first, under Status.

### Up Next

On an iPhone held upright, the first entry fills the top of the screen,
with Resume (or Play) and Remove from Up Next, and the rest are listed
under it. Tap a row to play it. Touch and hold one to see it larger, with
Play from Beginning, Go to Movie or Go to Show, Clear Progress, and Remove
from Up Next. On iOS 27 and later, swipe a row left to remove it.

On an iPad, or an iPhone on its side, it's the Apple TV's layout: the list
down the left, and the row you tap filling the rest, with its buttons.

### Movies and TV

Poster grids, sorted with the button at the top right: Alphabetically,
Year, Date Added, Last Watched, then Runtime (movies) or Unwatched Episodes
(shows). Pick the current sort again to reverse it. Touch and hold a
movie's poster to play it or mark it watched.

A movie's or show's page is as on the Apple TV. Tap an episode to play it;
touch and hold it to play from the beginning or mark it watched, or on iOS
27 and later, swipe it right. Touch and hold Resume for Play from
Beginning. Tapping the tab you're on goes back to its first page.

### Search

The magnifying glass: at the end of the tab bar, at the top of the iPad's
sidebar, and on its own beside the other tabs in a narrow window. It finds
what [the Apple TV's search](#search-1) does, as you type, under **Movies**,
**TV** and **Episodes**; the scope bar narrows them to one of those, or
**All**. Tapping an episode opens its show's page there: on its season,
scrolled to it.

### The player

The player turns to landscape as it opens on an iPhone. Turn the phone
upright to watch that way.

| | |
|---|---|
| Tap | show or hide the controls |
| Double-tap the left or right third | back or forward 10 seconds |
| Double-tap the middle, or pinch | fill the screen, or fit it |
| Drag the bar | move through the video, seeking when you let go |
| Swipe down | close |
| On an iPad's keyboard | space to play or pause, ← and → to skip 10 seconds, Escape to close |

The button at the top right has the subtitles and audio tracks. Subtitles,
saving your progress, the end of a movie or episode, and losing the
connection work as on the Apple TV. A phone call, Siri or an alarm pauses
it, as does unplugging headphones; leaving the app pauses it and saves your
progress, and it's paused where you left it when you come back. Control
Center and the Lock Screen show what's playing, and can play, pause, skip
and move through it while the app's open. There's no Picture in Picture,
and no playing in the background: once you've left the app, its controls
there do nothing until you come back to it.

The app follows the system's text size, VoiceOver, Reduce Motion and, on an
iPad, the pointer.

## Mac

Not yet, but it's planned.

## Terminal

A placeholder. A terminal client is planned: browse the library and Up Next
from a terminal and play through mpv (or VLC), reporting progress back to
the server.

Until then, from a terminal:

- `mvd progress list` shows Up Next, and `mvd progress watched` marks things
  watched (see [COMMANDS.md](COMMANDS.md#mvd-progress)).
- `mvd search` finds movies, shows and episodes, with their ids.
- `mvd upload` sends files to the server.
- A movie's or episode's `.m3u` link plays in mpv: `mpv 'http://router:6837/…'`.

## Any other player

Every movie and episode page on the website has an `.m3u` link: click it,
or copy it into VLC, mpv or IINA, which play anything. The stream supports
seeking. With a token set, the link is signed: it plays that one movie or
episode, and keeps working (saved in a playlist, or in the player's
history) until the token changes. It doesn't contain the token, but
anyone who has the link can play that file.

These players don't report progress back to mvd, so what you watch this way
isn't remembered. Use `mvd progress watched` or the website's watched button
to mark it.
