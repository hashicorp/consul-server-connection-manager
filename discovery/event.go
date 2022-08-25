package discovery

import "sync"

// event is a one time event that can be marked done.
type event struct {
	done chan struct{}
	once sync.Once
}

func newEvent() *event {
	return &event{
		done: make(chan struct{}),
	}
}

// SetDone marks the event as done.
// After this, Done() returns a closed channel.
// This is safe to call multiple times, concurrently.
func (e *event) SetDone() {
	e.once.Do(func() {
		close(e.done)
	})
}

// Done returns a channel that is closed once this event is done.
func (e *event) Done() <-chan struct{} {
	return e.done
}

// IsDone returns true if the event is done.
func (e *event) IsDone() bool {
	select {
	case <-e.Done():
		return true
	default:
		return false
	}
}
