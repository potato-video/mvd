#!/bin/sh
# Puts a library and a running mvd server in a known state: when each
# title was added, and what's been watched, as a plan file says. For the
# demo server and for tests; never your own library, as it forgets every
# bit of watch history the server has for the token.
#
#   scripts/reset-library.sh [-u URL] [-t TOKEN] [-a NAME] [-w SECONDS] PLAN LIBRARY
#
#   -u URL      the server (default http://localhost:6838, mvd-demogen test's)
#   -t TOKEN    the server's admin token ([server] token; none by default)
#   -a NAME     reset the watch history of the [[server.tokens]] entry NAME,
#               not the admin's
#   -w SECONDS  how long to wait for the server to read the videos' lengths
#               (default 60): 0 for stand-ins it can't read, taken to be
#               as long as their runtimes
#
# LIBRARY is the folder holding movies/ and tv/, on this machine: the
# added dates are the videos' modification times. In order, it
#
#   1. sets the added dates the plan gives, relative to now (mvd's "new"
#      is the last 30 days, so a reset weeks later still has new titles),
#   2. has the server read the library again,
#   3. forgets every movie's and show's watch history, and
#   4. plays back the plan's watch history, line by line: the last line
#      is the most recently watched, which orders Up Next.
#
# The plan has a line per path, with fields split by "|":
#
#   # path, in the library  | added, ago | watched
#   movies                  | 400d       |
#   movies/Juror Eleven (1977) | 3d      | 25m
#   tv/Night Drop (2019)/Season 01 | 300d | watched
#   tv/Night Drop (2019)/Season 02 | 6d  |
#   tv/Bus Pop! (2021)/Season 02/*S02E02* | | 4m
#
# A path is a movie's folder, a show's, a season's, an episode's video,
# or anything above them (movies, tv), and may be a shell pattern. Added
# applies to every video under it, later lines over earlier ones; leave it
# empty to keep the dates as they are. Watched is "watched" or how far in
# it was stopped; leave it empty for unwatched. Times are seconds, or with
# units: 90s, 25m, 1h52m, 3d. Watch history needs a movie, show or
# episode: a show's or season's "watched" marks each of its episodes.
#
# How far in is of the NFO's runtime, whatever the video's length: within
# a minute of the end, it's watched instead. It's scaled to the video, as
# the server found it, so a demo library with short videos resets the
# same: 14m into a 28-minute episode cut short at 5m is 2m30s in. (The
# server reads every video whose date changed again: this waits up to a
# minute in all for that.) Then the server's own rules apply: the last
# three minutes or 10% count as watched, and under resume_after (a minute
# by default) isn't a resume point, so with short videos, an early point
# can be lost.
set -eu

url=http://localhost:6838 token="" as="" wait=60
while getopts u:t:a:w: opt; do
    case $opt in
    u) url=${OPTARG%/} ;;
    t) token=$OPTARG ;;
    a) as=$OPTARG ;;
    w) wait=$OPTARG
        case $wait in "" | *[!0-9]*) echo "reset-library: -w takes seconds" >&2; exit 2 ;; esac ;;
    *) exit 2 ;;
    esac
done
shift $((OPTIND - 1))
[ $# -eq 2 ] || { sed -n '7,15p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
plan=$1 lib=${2%/}
[ -f "$plan" ] || { echo "reset-library: no plan at $plan" >&2; exit 1; }
[ -d "$lib/movies" ] || [ -d "$lib/tv" ] || { echo "reset-library: no movies/ or tv/ in $lib" >&2; exit 1; }

die() { echo "reset-library: $*" >&2; exit 1; }

# api METHOD PATH [JSON]: a request, as the token (and NAME, if given).
# The rescan is the admin's own, so it goes without NAME.
api() {
    who=$as
    [ "$2" = /api/rescan ] && who=""
    curl -sf -X "$1" ${token:+-H "Authorization: Bearer $token"} ${who:+-H "X-Mvd-As: $who"} \
        ${3:+-H "Content-Type: application/json" -d "$3"} "$url$2"
}

# secs TIME: "1h52m" (or 6720) as seconds.
secs() {
    echo "$1" | grep -Eq '^([0-9]+[dhms])*[0-9]*$' && [ -n "$1" ] || die "not a time: $1"
    echo $(($(echo "$1" | sed 's/d/*86400+/g; s/h/*3600+/g; s/m/*60+/g; s/s/+/g; s/+$//')))
}

# stamp EPOCH: for touch -t. BSD date (macOS), else GNU or BusyBox.
stamp() { date -r "$1" +%Y%m%d%H%M.%S 2>/dev/null || date -d "@$1" +%Y%m%d%H%M.%S; }

sha1() { if command -v sha1sum >/dev/null; then sha1sum; else shasum -a 1; fi; }

# videos DIR...: every video under them, as mvd sees them (none in a
# folder starting with "_" or ".").
videos() {
    find "$@" \( -type d \( -name '_*' -o -name '.*' \) -prune \) -o -type f \( \
        -iname '*.mkv' -o -iname '*.mp4' -o -iname '*.m4v' -o -iname '*.mov' -o -iname '*.avi' \
        -o -iname '*.webm' -o -iname '*.wmv' -o -iname '*.mpg' -o -iname '*.mpeg' -o -iname '*.ts' \
        -o -iname '*.m2ts' -o -iname '*.ogv' -o -iname '*.vob' -o -iname '*.divx' -o -iname '*.iso' \
        \) ! -name '.*' -print | sort
}

# id PREFIX NFO REL: the id mvd gives it, from its NFO's TMDB id, else
# from where it is in the library (as mvd's index does).
id() {
    n=""
    [ -f "$2" ] && n=$(sed -n 's/.*<uniqueid[^>]*type="tmdb"[^>]*>[[:space:]]*\([0-9]*\)[[:space:]]*<.*/\1/p' "$2" | head -1)
    if [ -n "$n" ] && [ "$n" != 0 ]; then
        echo "$1$n"
    else
        echo "f$(printf %s "$3" | sha1 | cut -c1-12)"
    fi
}

# ids FILE: the ids the watch history goes on, one per line: a movie's
# folder, a show's, or each episode in a season's or under a video.
ids() {
    rel=${1#"$lib"/}
    case $rel in
    movies/*/*) die "$rel: give a movie's folder" ;;
    movies/*)
        nfo=$(ls "$1"/*.nfo 2>/dev/null | head -1)
        id m "$nfo" "$rel" ;;
    tv/*/*)
        videos "$1" | while IFS= read -r f; do
            id e "${f%.*}.nfo" "${f#"$lib"/}"
        done ;;
    tv/*) id s "$1/tvshow.nfo" "$rel" ;;
    *) die "$rel: watch history needs a movie, a show or an episode" ;;
    esac
}

# Each line's path, as the files it matches: the pattern expanded, split
# only on newlines.
matches() {
    old=$IFS
    IFS='
'
    # shellcheck disable=SC2086
    set -- "$lib"/$1
    IFS=$old
    for f in "$@"; do
        [ -e "$f" ] || die "nothing in the library at ${f#"$lib"/}"
        echo "$f"
    done
}

trim() { echo "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'; }

# check PATH WATCHED: whether a line's watched column fits its path.
check() {
    case $2 in
    "" | watched) return ;;
    esac
    secs "$2" >/dev/null
    case ${1#"$lib"/} in
    movies/*) ;;
    *) [ -f "$1" ] || die "${1#"$lib"/}: how far in needs a movie or an episode" ;;
    esac
}

# lengths ID: its runtime (the NFO's) and its videos' length (what the
# server probed), in seconds, 0 for one that isn't known. The server may
# take a while to probe, so this waits for it until the deadline, a
# minute after the rescan (it may have no ffprobe, or a file it can't
# read).
lengths() {
    while :; do
        j=$(api GET "/api/item/$1") || die "the server has nothing at $1: is LIBRARY the server's?"
        # Only the files' lengths, not where it was stopped.
        f=$(echo "$j" | sed -n 's/.*"files":\[//p' | grep -o '"duration":[0-9.]*' | cut -d: -f2 |
            awk '{ s += $1 } END { printf "%d", s }')
        [ "$f" -eq 0 ] && [ "$(date +%s)" -lt "$deadline" ] || break
        sleep 0.5
    done
    r=$(echo "$j" | grep -o '"runtime":[0-9]*' | head -1 | cut -d: -f2)
    echo $((${r:-0} * 60)) "$f"
}

# played ID WATCHED PATH: sets one movie's, show's or episode's watch history.
# How far in is of its runtime, scaled to its video's length: 14m into a
# 28-minute episode whose video is cut short at 5m is 2m30s in.
played() {
    b='{"watched": true}'
    if [ "$2" != watched ]; then
        p=$(secs "$2")
        l=$(lengths "$1")
        r=${l% *} f=${l#* }
        if [ "$r" -eq 0 ]; then
            [ "$f" -gt 0 ] || die "$3: no runtime in its NFO, and the server hasn't read its video"
            r=$f
        elif [ "$f" -eq 0 ]; then
            [ "$wait" -eq 0 ] || echo "reset-library: $3: the server hasn't read its video: taking it to be its runtime" >&2
            f=$r
        fi
        if [ "$p" -lt $((r - 60)) ]; then
            s=$((p * f / r))
            b="{\"position\": $s, \"duration\": $f}"
            [ "$s" -ge 60 ] || echo "reset-library: $3: $2 in is ${s}s into its ${f}s video:" \
                "under resume_after (a minute by default), mvd won't keep it" >&2
        fi
    fi
    api POST "/api/progress/$1" "$b" >/dev/null || die "the server has nothing at $1: is LIBRARY the server's?"
}

# The plan's lines, path|added|watched, without comments or blanks.
lines=$(sed 's/#.*//' "$plan" | grep -v '^[[:space:]]*$' || true)
[ -n "$lines" ] || die "$plan is empty"

# Every line checked before anything changes.
echo "$lines" | while IFS='|' read -r path added watch; do
    path=$(trim "$path") added=$(trim "$added") watch=$(trim "$watch")
    [ -z "$added" ] || secs "$added" >/dev/null
    ms=$(matches "$path")
    echo "$ms" | while IFS= read -r m; do
        check "$m" "$watch"
        [ -z "$watch" ] || ids "$m" >/dev/null
    done
done
api GET /api/status >/dev/null || die "no answer from $url (is it running, and the token right?)"

# 1. Added.
now=$(date +%s)
echo "$lines" | while IFS='|' read -r path added watch; do
    added=$(trim "$added")
    [ -n "$added" ] || continue
    t=$(stamp $((now - $(secs "$added"))))
    ms=$(matches "$(trim "$path")")
    echo "$ms" | while IFS= read -r m; do
        videos "$m" | while IFS= read -r f; do touch -t "$t" "$f"; done
    done
done

# 2. The server reads it again.
api POST /api/rescan >/dev/null || die "the server didn't read the library again (is the token the admin's?)"
deadline=$(($(date +%s) + wait))

# 3. Everything forgotten. A show's watched=false is each episode's, and
# brings the show back to Up Next if it was taken out.
n=0
for list in movies shows; do
    for i in $(api GET "/api/$list" | grep -o '"id": *"[^"]*"' | sed 's/.*"\([^"]*\)"$/\1/'); do
        api POST "/api/progress/$i" '{"watched": false}' >/dev/null || die "couldn't clear $i"
        n=$((n + 1))
    done
done
echo "reset-library: forgot the watch history of $n movies and shows"

# 4. The watch history, in order.
echo "$lines" | while IFS='|' read -r path added watch; do
    watch=$(trim "$watch")
    [ -n "$watch" ] || continue
    ms=$(matches "$(trim "$path")")
    echo "$ms" | while IFS= read -r m; do
        ids "$m" | while IFS= read -r i; do played "$i" "$watch" "${m#"$lib"/}"; done
    done
done
echo "reset-library: $url is as $plan says"
