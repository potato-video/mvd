package deploy

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"mvd/internal/config"
)

// On a router mvd keeps to 15MB, so Transmission isn't the one the kernel
// kills when memory runs out.
func TestOpenWRTMemoryLimit(t *testing.T) {
	if !regexp.MustCompile(`(?m)^\tprocd_set_param env HOME=/root GOMEMLIMIT=15MiB\b`).MatchString(OpenWRT) {
		t.Errorf("no GOMEMLIMIT=15MiB in the procd script:\n%s", OpenWRT)
	}
}

// The example has every setting mvd knows, in its section, and no others.
func TestConfigExampleSettings(t *testing.T) {
	section := regexp.MustCompile(`^#?\[\[?([\w.]+)\]\]?$`)
	setting := regexp.MustCompile(`^#?(\w+)\s*=`)
	var got []string
	sec := ""
	for _, line := range strings.Split(ConfigExample, "\n") {
		if m := section.FindStringSubmatch(line); m != nil {
			sec = m[1]
		} else if m := setting.FindStringSubmatch(line); m != nil {
			got = append(got, sec+"."+m[1])
		}
	}
	slices.Sort(got)
	if want := config.Settings(); !slices.Equal(got, want) {
		t.Errorf("example has\n  %v\nmvd knows\n  %v", got, want)
	}
}
