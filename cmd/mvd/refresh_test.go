package main

import (
	"slices"
	"strings"
	"testing"
)

// Which libraries refresh covers: named, or implied by -movie (movies) and
// -show, -season or -order (TV).
func TestRefreshKinds(t *testing.T) {
	both, movies, tv := []bool{false, true}, []bool{false}, []bool{true}
	for _, c := range []struct {
		args string
		want []bool
	}{
		{"", both},
		{"-n -all", both},
		{"movies", movies},
		{"tv -n", tv},
		{"-movie godfather", movies},
		{"movies -movie godfather", movies},
		{"-show batman", tv},
		{"--show=batman -season 2", tv},
		{"-order dvd -show batman", tv},
		{"tv -show batman -season=0", tv},
		{"tv -movie godfather", nil},
		{"movies -show batman", nil},
		{"movies -season 1", nil},
		{"movies -order dvd", nil},
		{"-movie godfather -show batman", nil},
		{"-movie godfather -season 1", nil},
	} {
		got, _, err := refreshKinds(strings.Fields(c.args))
		if c.want == nil {
			if err == nil {
				t.Errorf("%q: %v, want an error", c.args, got)
			}
		} else if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: %v %v, want %v", c.args, got, err, c.want)
		}
	}
}

// Flag mistakes stop before anything is loaded.
func TestRefreshUsage(t *testing.T) {
	for _, args := range []string{
		"tv -season 2",                      // no -show
		"-show batman -season two",          // not a number
		"-show batman -season -1",           // nor negative
		"-show batman -season 2 -order dvd", // the order is for the whole show
		"-movie godfather -order dvd",
		"tv -movie godfather",
	} {
		if rc := cmdRefresh(strings.Fields(args)); rc != 2 {
			t.Errorf("%q: rc=%d, want 2", args, rc)
		}
	}
}
