package discovery

import (
	"sync"
	"time"
)

type mockClock struct {
	sync.Mutex

	t time.Time

	// this stores
	afterChans map[time.Time]chan time.Time
}

var _ Clock = (*mockClock)(nil)

// You must call Sleep to advance the mock clock.
func (f *mockClock) After(d time.Duration) <-chan time.Time {
	f.Lock()
	defer f.Unlock()

	ch := make(chan time.Time, 1)
	if d == 0 {
		// Channel will immediately return on 0 duration.
		ch <- f.t
		close(ch)
		return ch
	} else {
		// Otherwise, handle the send at the next Tick
		end := f.t.Add(d)
		f.afterChans[end] = ch
	}
	return ch
}

func (f *mockClock) Now() time.Time {
	f.Lock()
	defer f.Unlock()

	return f.t
}

func (f *mockClock) Sleep(d time.Duration) {
	f.Lock()
	defer f.Unlock()

	f.t = f.t.Add(d)

	// Send to channels returned by After()
	for end, ch := range f.afterChans {
		if f.t.After(end) {
			// non-blocking send
			select {
			case ch <- f.t:
			default:
			}

			close(ch)
			delete(f.afterChans, end)
		}
	}
}
