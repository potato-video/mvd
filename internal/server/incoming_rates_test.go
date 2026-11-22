//go:build webui && files

package server

import "testing"

// Review L6: an upload's speed sample goes when the upload does, finished
// while going or not.
func TestUploadRatesDontGrow(t *testing.T) {
	ts, _ := uploadServer(t)
	u := ts.startUpload(`{"name": "A.mkv", "size": 10, "fingerprint": "a"}`, 201)
	ts.put(u.ID, 0, make([]byte, 5))
	ts.sv.incomingUploads() // first seen going
	ts.put(u.ID, 5, make([]byte, 5))
	ts.sv.incomingUploads() // finished: gone from the list
	ts.sv.ups.mu.Lock()
	n := len(ts.sv.ups.rates)
	ts.sv.ups.mu.Unlock()
	if n != 0 {
		t.Errorf("%d speed samples kept for finished uploads", n)
	}
}
