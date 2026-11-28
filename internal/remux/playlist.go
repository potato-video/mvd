package remux

import (
	"bytes"
	"fmt"
	"math"
	"sort"
)

// Playlist is an HLS VOD playlist with one segment per keyframe, so it's
// whole from the start and a player can seek anywhere in it. base is
// what each URI starts with ("/remux/m238/"), query what it ends with
// ("?part=1&audio=0"). Segment N is "%05d.m4s", as ffmpeg names them.
// start, when it's past the beginning, is where the player is to start, so
// Safari doesn't fetch the first segment before it seeks.
func (ix *Index) Playlist(base, query string, start float64) []byte {
	var b bytes.Buffer
	most := 0.0
	for i := range ix.Keyframes {
		most = math.Max(most, ix.length(i))
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(most)))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	if start > 0 {
		fmt.Fprintf(&b, "#EXT-X-START:TIME-OFFSET=%.3f,PRECISE=YES\n", start)
	}
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", base, query)
	for i := range ix.Keyframes {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s%05d.m4s%s\n", ix.length(i), base, i, query)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}

// length is segment i's length: to the next keyframe, or the end.
func (ix *Index) length(i int) float64 {
	if i+1 < len(ix.Keyframes) {
		return ix.Keyframes[i+1] - ix.Keyframes[i]
	}
	return ix.Duration - ix.Keyframes[i]
}

// Segment is the segment playing at t seconds into the playlist, which
// starts at the first keyframe.
func (ix *Index) Segment(t float64) int {
	return max(sort.SearchFloat64s(ix.Keyframes, ix.Keyframes[0]+t+0.0005)-1, 0)
}

// seekTo is where ffmpeg is asked to start for segment n: partway into it,
// since it starts from the keyframe at or before the time asked for.
func (ix *Index) seekTo(n int) float64 {
	return ix.Keyframes[n] + math.Min(ix.length(n)/2, 0.5)
}
