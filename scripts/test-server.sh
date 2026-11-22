#!/bin/sh
# A throwaway mvd server for trying the Apple app (or the website, at
# http://localhost:6838/, signing in with "test") against a long Up Next:
# fifty made-up movies, all "new" (added over the last few weeks), a few
# part-watched, each with its own coloured poster and fanart; and twenty
# made-up shows, some part-way through, so Up Next also has NEXT EPISODE,
# episode CONTINUE WATCHING and NEW show rows, and the show pages have
# anything from one episode to eight seasons (with specials, a named
# season, and a still for every episode). The movies' files are stand-ins,
# but for one: Final Reel (m900050) is a real twenty-second clip with two
# subtitle tracks of its own (English, and German as its default) and a
# French sidecar, for trying the player, and for the UI tests
# (mvd-apple's MVD-tvOS-UITests). Nothing of yours is touched: the library,
# state and config all live in one folder (default /tmp/mvd-test), and the
# server listens on :6838 with token "test".
#
#   scripts/test-server.sh [folder]
#
# In the app (the simulator shares the Mac's network): address
# localhost:6838, token test. Up Next shows at most 40 (upNextLimit).
#
# When each title arrived and what's been watched are a plan for
# scripts/reset-library.sh, written as the library is (reset.txt in the
# folder), so it can be put back as it started while the server runs:
#
#   scripts/reset-library.sh -t test -w 0 /tmp/mvd-test/reset.txt /tmp/mvd-test
set -eu

root=${1:-/tmp/mvd-test}
here=$(cd "$(dirname "$0")/.." && pwd)  # the repo
port=6838
token=test
plan=$root/reset.txt

command -v ffmpeg >/dev/null || { echo "test-server: needs ffmpeg for the artwork" >&2; exit 1; }

# art FILE WxH HUE: a picture in its own colours, so crossfades show.
# Drawn at a quarter of the size and scaled up: the gradients are smooth,
# and geq, a pixel at a time, is most of the script's time. -nostdin, or
# ffmpeg eats the lines the loops below are reading.
art() {
    w=${2%x*} h=${2#*x}
    ffmpeg -nostdin -loglevel error -y -f lavfi -i "color=c=black:s=$((w / 4))x$((h / 4))" -frames:v 1 \
        -vf "geq=r='128+100*sin(2*PI*(X/W+$3/360))':g='128+100*sin(2*PI*(Y/H+$3/360+0.33))':b='128+100*sin(2*PI*($3/360+0.66))',scale=$w:$h" \
        "$1"
}

rm -rf "$root"
mkdir -p "$root/movies" "$root/tv" "$root/config/mvd" "$root/data"
echo '# path | added, ago | watched: the plan for scripts/reset-library.sh' >"$plan"

cat >"$root/config/mvd/config.toml" <<EOF
[paths]
movies_dir = "$root/movies"
tv_dir = "$root/tv"
data_dir = "$root/data"
cache_dir = "$root/data/cache"
state_file = "$root/data/state.json"
index_file = "$root/data/index.json"

[server]
port = $port
token = "$token"

[library]
# Final Reel is twenty seconds long: keep where it got to.
resume_after = "1s"
EOF

# Some long titles among them, to see the rows truncate.
titles="The Long Weekend|Harbour Lights|A Quiet Engine|Northern Line|The Glass Orchard|Ten Miles of Fog|Paper Kingdoms|The Night Ferry|Small Hours|Cobalt|The Lighthouse Keeper's Unusually Long Last Summer Holiday|Saltmarsh|Brass Monkeys|The Fourth Signal|Low Tide|Velvet Underpass|An Accidental Heir|The Cartographer|Rainy Season|Kestrel|Westbound|The Silent Partner|Under the Viaduct|Hollow Crown Street|Marigold|The Last Tram|Iron and Ivy|Deep Field|The Understudy|Copperhead|Midsummer Static|The Winter Garden|Signal Box|Foxglove|The Seventh Floor|A Map of Small Places|Heatwave|The Ferryman's Daughter and the Strange Affair at Pembroke Lock|Lantern|The Weather Station|Gull Island|Second Sight|The Dark Meridian|Overcast|Parallel Lines|The Bell Foundry|Shoreditch Nights|Amber Alert|The Orchard Keeper|Final Reel"
genres="Drama|Thriller|Comedy|Crime|Science Fiction|Romance|Mystery|Adventure"
certs="U|PG|12A|15|18"

i=0
echo "$titles" | tr '|' '\n' | while IFS= read -r title; do
    i=$((i + 1))
    year=$((1960 + (i * 7) % 64))
    name="$title ($year)"
    dir="$root/movies/$name"
    mkdir -p "$dir"
    printf '\032\105\337\243 test' >"$dir/$name.mkv"  # just the Matroska magic
    genre=$(echo "$genres" | cut -d'|' -f$(((i % 8) + 1)))
    cert=$(echo "$certs" | cut -d'|' -f$(((i % 5) + 1)))
    cat >"$dir/$name.nfo" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>$title</title>
  <year>$year</year>
  <plot>Test movie number $i. A made-up plot, long enough to wrap over a couple of lines on the feature, so the text and the backdrop's gradients can be judged together against a picture.</plot>
  <runtime>$((85 + (i * 13) % 70))</runtime>
  <mpaa>$cert</mpaa>
  <uniqueid type="tmdb" default="true">$((900000 + i))</uniqueid>
  <genre>$genre</genre>
</movie>
EOF
    # A different hue for each, so the crossfades show.
    hue=$(((i * 47) % 360))
    art "$dir/fanart.jpg" 1920x1080 $hue
    art "$dir/poster.jpg" 600x900 $hue
    # Added over the last 25 days, a new one every half day; a few
    # part-watched (CONTINUE WATCHING), 30 minutes in.
    watch=""
    case $i in 3 | 11 | 27) watch=30m ;; esac
    echo "movies/$name | $((i * 12))h | $watch" >>"$plan"
done

# Final Reel, the last, is a real clip: a test pattern and a tone, with
# subtitles of its own and a sidecar, each saying which it is.
reel="$root/movies/Final Reel ($((1960 + (50 * 7) % 64)))"
srt() { printf '1\n00:00:01,000 --> 00:00:19,000\n%s\n' "$1"; }
srt "English, the file's own" >"$root/en.srt"
srt "German, the file's own default" >"$root/de.srt"
ffmpeg -nostdin -loglevel error -y -f lavfi -i testsrc2=size=640x360:rate=25 -f lavfi -i sine=frequency=440 \
    -i "$root/en.srt" -i "$root/de.srt" -t 20 -map 0 -map 1 -map 2 -map 3 \
    -c:v libx264 -preset ultrafast -c:a aac -c:s srt \
    -metadata:s:s:0 language=eng -metadata:s:s:1 language=ger -disposition:s:0 0 -disposition:s:1 default \
    "$reel/$(basename "$reel").mkv"
rm "$root/en.srt" "$root/de.srt"
srt "French, the server's sidecar" >"$reel/$(basename "$reel").fr.srt"

echo "test-server: $(ls "$root/movies" | wc -l | tr -d ' ') movies in $root/movies"

# Shows: number|title|year|genres|certificate|status|runtime|seasons, where
# each season is number:episodes:hours ago its first episode arrived, the
# rest a day apart. Old seasons aren't new; Allotment Wars' fourth season
# and all of Copper Beeches are. What's watched is in the plan, below.
eptitles="The Crossing|Low Water|Night Shift|Small Mercies|The Long Way Round|Ebb|Flood|Dead Reckoning|An Unusually Long Episode Title About a Very Particular Tuesday Afternoon|Second Chances|The Inspector Calls|Fog Bank|Homecoming|Loose Ends|Spring Tide|Crossfire|The Reckoning|Old Friends|Harvest|First Frost|Last Orders|Breakwater|Open Water|Slack Tide"
while IFS='|' read -r num title year genres cert status runtime seasons; do
    dir="$root/tv/$title ($year)"
    mkdir -p "$dir"
    named=""
    [ "$num" = 3 ] && named='  <namedseason number="4">The Last Harvest</namedseason>'
    cat >"$dir/tvshow.nfo" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<tvshow>
  <title>$title</title>
  <year>$year</year>
  <plot>Test show number $num. A made-up plot, long enough to wrap over a couple of lines on the show page, so the text can be judged against the backdrop.</plot>
  <mpaa>$cert</mpaa>
  <status>$status</status>
  <uniqueid type="tmdb" default="true">$((910000 + num))</uniqueid>
$(echo "$genres" | tr ',' '\n' | sed 's|.*|  <genre>&</genre>|')
$named
</tvshow>
EOF
    hue=$(((num * 83 + 30) % 360))
    art "$dir/fanart.jpg" 1920x1080 $hue
    art "$dir/poster.jpg" 600x900 $hue

    for spec in $seasons; do
        s=${spec%%:*} rest=${spec#*:}
        count=${rest%%:*} first=${rest#*:}
        if [ "$s" = 0 ]; then
            folder=Specials poster=season-specials-poster.jpg
        else
            folder=$(printf 'Season %02d' "$s") poster=$(printf 'season%02d-poster.jpg' "$s")
        fi
        mkdir -p "$dir/$folder"
        art "$dir/$poster" 600x900 $(((hue + s * 40) % 360))
        e=1
        while [ $e -le "$count" ]; do
            code=$(printf 'S%02dE%02d' "$s" $e)
            eptitle=$(echo "$eptitles" | cut -d'|' -f$((((num - 1) * 11 + s * 6 + e) % 24 + 1)))
            base="$dir/$folder/$title ($year) - $code - $eptitle"
            printf '\032\105\337\243 test' >"$base.mkv"
            cat >"$base.nfo" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<episodedetails>
  <title>$eptitle</title>
  <showtitle>$title</showtitle>
  <season>$s</season>
  <episode>$e</episode>
  <plot>Episode $e of season $s of test show $num: its own made-up plot, not the show's.</plot>
  <aired>$((year + (s > 0 ? s - 1 : 0)))-$(printf %02d $(((e - 1) % 12 + 1)))-$(printf %02d $((e * 2)))</aired>
  <runtime>$runtime</runtime>
  <uniqueid type="tmdb" default="true">$((92000000 + num * 10000 + s * 100 + e))</uniqueid>
</episodedetails>
EOF
            art "$base-thumb.jpg" 1280x720 $(((hue + s * 40 + e * 25) % 360))
            echo "${base#"$root"/}.mkv | $((first - (e - 1) * 24))h |" >>"$plan"
            e=$((e + 1))
        done
    done
done <<EOF
1|Severn Crossing|2019|Drama,Thriller|15|Ended|45|1:6:4000 2:6:3000 3:6:2000
2|The Night Desk|2022|Crime,Drama|15|Returning Series|50|1:8:3000 2:8:1500
3|Allotment Wars|2014|Comedy|PG|Returning Series|30|0:2:5000 1:5:6000 2:5:5000 3:5:4000 4:5:130
4|Copper Beeches|2026|Mystery,Drama|12|Returning Series|55|1:6:150
5|The Lighthouse Keepers' Remarkably Long Chronicle of the Western Approaches|2017|Drama|12|Ended|30|1:10:2500
6|Kingfisher Lane|2011|Drama,Romance|12|Ended|50|1:6:9000 2:6:8500 3:6:8000
7|Dockside|2016|Crime|15|Ended|60|1:4:7000
8|Pennine Way|2020|Documentary|U|Ended|30|1:8:6000
9|Cold Case Café|2021|Comedy,Crime|12|Returning Series|25|1:10:5500 2:10:4500
10|The Marsh Wardens|2018|Drama,Mystery|15|Ended|55|1:6:7500 2:6:6500
11|Brightwater|2008|Drama|12|Ended|45|1:3:12000 2:3:11500 3:3:11000 4:3:10500 5:3:10000 6:3:9500 7:3:9000 8:3:8500
12|Signal Failure|2025|Thriller|15|Returning Series|50|1:6:3500 2:4:200
13|Night Bus|2023|Comedy|15|Returning Series|30|1:6:4500
14|The Long Field|2015|Drama|PG|Ended|90|1:1:8000
15|Starling|2019|Science Fiction|12|Returning Series|45|0:3:5000 1:8:6000 2:8:5000
16|Tidewater Station|2012|Mystery|12|Ended|50|1:6:9500 2:6:9000
17|Quayside|2024|Romance,Comedy|12|Returning Series|40|1:8:2800
18|Fen Country|2010|Crime,Drama|15|Ended|60|1:4:10000 2:4:9800 3:4:9600
19|Watchmakers|2022|Documentary|U|Returning Series|30|1:6:3800 2:6:3200
20|Hollow Hill|2017|Horror|18|Ended|45|1:6:7000 2:6:6000
EOF

echo "test-server: $(ls "$root/tv" | wc -l | tr -d ' ') shows in $root/tv"

# What's been watched in the shows, after the movies, so the shows are the
# most recently watched. Resume points are of each episode's runtime.
cat >>"$plan" <<'EOF'
tv/Severn Crossing (2019)/Season 01                 | | watched
tv/Severn Crossing (2019)/Season 02/*S02E0[12]*     | | watched
# The Night Desk: CONTINUE S01E04
tv/The Night Desk (2022)/Season 01/*S01E0[1-3]*     | | watched
tv/The Night Desk (2022)/Season 01/*S01E04*         | | 25m
# Allotment Wars: NEXT S04E01, 5 new
tv/Allotment Wars (2014)/Season 0[1-3]              | | watched
# The Lighthouse Keepers': CONTINUE S01E07
tv/The Lighthouse Keepers' Remarkably Long Chronicle of the Western Approaches (2017)/Season 01/*S01E0[1-6]* | | watched
tv/The Lighthouse Keepers' Remarkably Long Chronicle of the Western Approaches (2017)/Season 01/*S01E07* | | 10m
# Copper Beeches: NEW, 6 new
# Dockside: all watched, not in Up Next
tv/Dockside (2016)                                  | | watched
# Cold Case Café: NEXT S01E02
tv/Cold Case Café (2021)/Season 01/*S01E01*         | | watched
# Starling: CONTINUE a special
tv/Starling (2019)/Specials/*S00E02*                | | 15m
# Signal Failure: NEW (S01E01), 4 new; the rest: in the TV grid only
EOF

cd "$here"
go build -tags 'webui files' -o "$root/mvd" ./cmd/mvd
XDG_CONFIG_HOME="$root/config" "$root/mvd" server &
pid=$!
trap 'kill $pid 2>/dev/null' EXIT INT TERM

# Once it answers, the plan. The stand-ins can't be read for their length,
# so they're taken to be as long as their runtimes (-w 0).
until curl -sf -H "Authorization: Bearer $token" "http://localhost:$port/api/status" >/dev/null; do sleep 0.3; done
scripts/reset-library.sh -t "$token" -w 0 "$plan" "$root"
echo "test-server: listening on :$port, token $token (Ctrl-C to stop)"
echo "test-server: to put it back as it started: scripts/reset-library.sh -t $token -w 0 $plan $root"
wait $pid
