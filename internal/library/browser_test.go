package library

import (
	"reflect"
	"testing"
)

func TestBrowser(t *testing.T) {
	v := func(codec, profile, tag string, bits int) Track {
		return Track{Kind: "video", Codec: codec, Profile: profile, Tag: tag, Bits: bits}
	}
	a := func(codec, profile string, def bool) Track {
		return Track{Kind: "audio", Codec: codec, Profile: profile, Default: def}
	}
	sub := Track{Kind: "subtitle", Codec: "subrip", Default: true}
	for name, c := range map[string]struct {
		mf   *MediaFile
		want *Browser
	}{
		"H.264 and AAC in MKV": {&MediaFile{Container: "mkv", Tracks: []Track{v("h264", "High", "", 8), a("aac", "LC", true), sub}},
			&Browser{Type: "video/x-matroska", Video: "avc1.640028", Audio: []string{"mp4a.40.2"}}},
		"the second audio track the default": {&MediaFile{Container: "mkv", Tracks: []Track{v("hevc", "Main 10", "", 10), a("truehd", "", false), a("ac3", "", true)}},
			&Browser{Type: "video/x-matroska", Video: "hvc1.2.4.L153.B0", Audio: []string{"mlpa", "ac-3"}, Default: 1}},
		"hev1 in MP4": {&MediaFile{Container: "mp4", Tracks: []Track{v("hevc", "Main", "hev1", 8), a("eac3", "", false)}},
			&Browser{Type: "video/mp4", Video: "hev1.1.6.L153.B0", Audio: []string{"ec-3"}}},
		"HEVC on the router, no profile": {&MediaFile{Container: "mkv", Tracks: []Track{v("hevc", "", "", 0)}},
			&Browser{Type: "video/x-matroska", Video: "hvc1.1.6.L153.B0", Audio: []string{}}},
		"Hi10P": {&MediaFile{Container: "mkv", Tracks: []Track{v("h264", "High 10", "", 10), a("flac", "", true)}},
			&Browser{Type: "video/x-matroska", Video: "avc1.6E0028", Audio: []string{"flac"}}},
		"Xvid in AVI": {&MediaFile{Container: "avi", Tracks: []Track{v("mpeg4", "Advanced Simple Profile", "XVID", 8), a("mp3", "", false)}},
			&Browser{Video: "", Audio: []string{"mp4a.6B"}}},
		"VP9 and Opus": {&MediaFile{Container: "webm", Tracks: []Track{v("vp9", "Profile 0", "", 8), a("opus", "", true), a("dts", "DTS-HD MA", false)}},
			&Browser{Type: "video/webm", Video: "vp09.00.40.08", Audio: []string{"opus", "dtsc"}}},
		"no video":     {&MediaFile{Container: "mkv", Tracks: []Track{a("aac", "", true)}}, nil},
		"not probed":   {nil, nil},
		"not readable": {&MediaFile{}, nil},
	} {
		if got := c.mf.Browser(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}
