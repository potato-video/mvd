package library

import "strings"

// What a browser is asked before it's given a file: its container's MIME
// type and its codecs as RFC 6381 names, for HTMLMediaElement.canPlayType.
// Which browser plays what changes too often for the server to keep a
// table, so the website asks the browser.

// Browser describes a probed file to a browser.
type Browser struct {
	// Type is the container's MIME type, "" for one no browser opens (AVI,
	// WMV, MPEG-TS).
	Type string `json:"type"`
	// Video is the first video track's codec, "" when it has no RFC 6381
	// name (MPEG-2, MPEG-4 Part 2), which no browser plays.
	Video string `json:"video"`
	// Audio is each audio track's codec, in order, "" for one with no name.
	Audio []string `json:"audio"`
	// Default is the audio track a browser plays, an index into Audio: the
	// one flagged default, else the first.
	Default int `json:"default"`
}

// Browser is nil for a file that wasn't probed or has no video.
func (mf *MediaFile) Browser() *Browser {
	if !mf.OK() {
		return nil
	}
	b := &Browser{Type: containerType(mf.Container), Audio: []string{}}
	video, def := false, -1
	for _, t := range mf.Tracks {
		switch t.Kind {
		case "video":
			if !video {
				video, b.Video = true, videoCodec(t, mf.Container)
			}
		case "audio":
			if t.Default && def < 0 {
				def = len(b.Audio)
			}
			b.Audio = append(b.Audio, audioCodec(t))
		}
	}
	if !video {
		return nil
	}
	b.Default = max(def, 0)
	return b
}

func containerType(c string) string {
	switch c {
	case "mp4", "m4v", "mov":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	}
	return ""
}

// videoCodec names a video track as RFC 6381 does, with the profile it has
// and a level high enough for 1080p (4K for HEVC): the level isn't probed,
// and browsers ask little of it.
func videoCodec(t Track, container string) string {
	switch t.Codec {
	case "h264":
		switch {
		case strings.Contains(t.Profile, "Baseline"):
			return "avc1.42E01E"
		case t.Profile == "Main":
			return "avc1.4D401F"
		case strings.HasPrefix(t.Profile, "High 4:4:4"):
			return "avc1.F40028"
		case t.Profile == "High 4:2:2":
			return "avc1.7A0028"
		case t.Profile == "High 10" || t.Bits > 8:
			return "avc1.6E0028" // Hi10P, which browsers don't play
		}
		return "avc1.640028"
	case "hevc":
		// MKV has no tag; a remux tags it hvc1, which Apple wants. An MP4
		// says which it has, and hev1 is why Safari won't play one.
		tag := "hvc1"
		if t.Tag == "hev1" && container != "mkv" && container != "webm" {
			tag = "hev1"
		}
		if t.Profile == "Main 10" || (t.Profile == "" && t.Bits > 8) {
			return tag + ".2.4.L153.B0"
		}
		return tag + ".1.6.L153.B0"
	case "av1":
		if t.Bits > 8 {
			return "av01.0.08M.10"
		}
		return "av01.0.08M.08"
	case "vp9":
		if t.Bits > 8 {
			return "vp09.02.40.10"
		}
		return "vp09.00.40.08"
	case "vp8":
		return "vp8"
	}
	return ""
}

func audioCodec(t Track) string {
	switch t.Codec {
	case "aac":
		switch t.Profile {
		case "HE-AAC":
			return "mp4a.40.5"
		case "HE-AACv2":
			return "mp4a.40.29"
		}
		return "mp4a.40.2"
	case "mp3":
		return "mp4a.6B"
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	case "opus":
		return "opus"
	case "flac":
		return "flac"
	case "vorbis":
		return "vorbis"
	case "alac":
		return "alac"
	case "dts":
		return "dtsc"
	case "truehd":
		return "mlpa"
	}
	return ""
}
