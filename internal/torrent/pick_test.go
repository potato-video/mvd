package torrent

import "testing"

// An all-digit argument is a torrent ID, not part of a name: the web UI's
// Resume button for torrent 2 mustn't also resume "2001 A Space Odyssey".
func TestPickByID(t *testing.T) {
	ts := []Torrent{{ID: 2, Name: "Severance S01"}, {ID: 7, Name: "2001 A Space Odyssey"}, {ID: 9, Name: "Alien 2"}}
	got, err := Pick(ts, []string{"2"}, nil)
	if err != nil || len(got) != 1 || got[0].ID != 2 {
		t.Errorf("Pick 2: %+v %v", got, err)
	}
	// A number that's no torrent's ID still finds names ("1979", say).
	got, err = Pick(ts, []string{"2001"}, nil)
	if err != nil || len(got) != 1 || got[0].ID != 7 {
		t.Errorf("Pick 2001: %+v %v", got, err)
	}
	got, err = Pick(ts, []string{"alien"}, nil)
	if err != nil || len(got) != 1 || got[0].ID != 9 {
		t.Errorf("Pick alien: %+v %v", got, err)
	}
}
