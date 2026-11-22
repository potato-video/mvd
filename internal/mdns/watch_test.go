package mdns

import (
	"sync/atomic"
	"testing"
	"time"
)

// One change comes as several events (an interface, then each of its
// addresses): the networks are looked at once, when they've settled.
func TestFollowSettles(t *testing.T) {
	events, stop := make(chan struct{}), make(chan struct{})
	var looks atomic.Int32
	go follow(events, 50*time.Millisecond, time.Hour, stop, func() { looks.Add(1) })
	for i := 0; i < 5; i++ {
		events <- struct{}{}
	}
	if looks.Load() != 0 {
		t.Error("looked before the change settled")
	}
	time.Sleep(200 * time.Millisecond)
	if n := looks.Load(); n != 1 {
		t.Errorf("looked %d times for one change", n)
	}
	events <- struct{}{}
	time.Sleep(200 * time.Millisecond)
	if n := looks.Load(); n != 2 {
		t.Errorf("looked %d times for two changes", n)
	}
	close(stop)
}

// Where the system doesn't say what changes, or stops saying, the networks
// are looked at now and then.
func TestFollowPolls(t *testing.T) {
	for name, events := range map[string]chan struct{}{"no events": nil, "events ended": make(chan struct{})} {
		stop := make(chan struct{})
		var looks atomic.Int32
		if events != nil {
			close(events)
		}
		go follow(events, time.Hour, 20*time.Millisecond, stop, func() { looks.Add(1) })
		time.Sleep(200 * time.Millisecond)
		close(stop)
		if n := looks.Load(); n < 3 {
			t.Errorf("%s: looked %d times in ten polls", name, n)
		}
	}
}
