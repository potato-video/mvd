package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// WriteClip writes a real MKV, about 3 KB, a second long, when ffmpeg is on
// the PATH, and returns false (writing nothing) when it isn't. Its tracks,
// in order:
//
//	video    h264 64x48, 16:9 (SAR 4:3), 24000/1001, flagged interlaced
//	audio    aac stereo, eng, default
//	audio    aac mono, eng, "Director", commentary
//	subtitle subrip, no language, default and forced
//	subtitle subrip, fre, hearing impaired
//	subtitle subrip, no tags at all
func WriteClip(t *testing.T, path string) bool {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return false
	}
	srt := filepath.Join(t.TempDir(), "clip.srt")
	Write(t, srt, "1\n00:00:00,000 --> 00:00:00,500\nHello\n")
	os.MkdirAll(filepath.Dir(path), 0o755)
	cmd := exec.Command(ff, "-v", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x48:d=1:r=24000/1001",
		"-f", "lavfi", "-i", "anullsrc=r=8000:cl=stereo",
		"-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono",
		"-i", srt, "-i", srt, "-i", srt, "-t", "1",
		"-map", "0", "-map", "1", "-map", "2", "-map", "3", "-map", "4", "-map", "5",
		"-c:v", "libx264", "-preset", "ultrafast", "-aspect", "16:9", "-field_order", "tt", "-c:a", "aac", "-c:s", "srt",
		"-map_metadata", "-1", "-fflags", "+bitexact",
		"-metadata:s:a:0", "language=eng", "-disposition:a:0", "default",
		"-metadata:s:a:1", "language=eng", "-metadata:s:a:1", "title=Director", "-disposition:a:1", "comment",
		"-metadata:s:s:0", "language=und", "-disposition:s:0", "default+forced",
		"-metadata:s:s:1", "language=fre", "-disposition:s:1", "hearing_impaired",
		"-metadata:s:s:2", "language=und", "-disposition:s:2", "0",
		"-f", "matroska", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return true
}
