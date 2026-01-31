// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"sync"
	"time"
)

type mockClock struct {
	sync.Mutex

	currentTime time.Time

	// afterChans stores channels in order to mimic time.After. Each channnel
	// is stores with the timestamp when the channel should return.
	afterChans map[chan time.Time]time.Time
}

var _ Clock = (*mockClock)(nil)

// You must call Sleep to advance the mock clock.
func (f *mockClock) After(d time.Duration) <-chan time.Time {
	f.Lock()
	defer f.Unlock()

	ch := make(chan time.Time, 1)
	if d == 0 {
		// Channel will immediately return on 0 duration.
		ch <- f.currentTime
		close(ch)
		return ch
	} else {
		// Otherwise, handle the send at the next Tick
		f.afterChans[ch] = f.currentTime.Add(d)
	}
	return ch
}

func (f *mockClock) Now() time.Time {
	f.Lock()
	defer f.Unlock()

	return f.currentTime
}

func (f *mockClock) Sleep(d time.Duration) {
	f.Lock()
	defer f.Unlock()

	f.currentTime = f.currentTime.Add(d)

	// Send to channels returned by After()
	for ch, end := range f.afterChans {
		if f.currentTime.After(end) {
			// non-blocking send
			select {
			case ch <- f.currentTime:
			default:
			}

			close(ch)
			delete(f.afterChans, ch)
		}
	}
}
