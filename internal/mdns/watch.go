package mdns

import "time"

// follow calls refresh when the machine's networks may have changed, until
// stop is closed. events is the system saying so (changes), and often
// several times over for one change (an interface coming up gets its
// addresses one by one), so it's one refresh, settle after the first. With
// no events (nil, or the channel closes: the system doesn't say, or
// stopped saying), it's a refresh every poll.
func follow(events <-chan struct{}, settle, poll time.Duration, stop <-chan struct{}, refresh func()) {
	var settled, polling <-chan time.Time
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		if events == nil && ticker == nil {
			ticker = time.NewTicker(poll)
			polling = ticker.C
		}
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			} else if settled == nil {
				settled = time.After(settle)
			}
		case <-settled:
			settled = nil
			refresh()
		case <-polling:
			refresh()
		case <-stop:
			return
		}
	}
}
