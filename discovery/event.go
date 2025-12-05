// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"context"
	"sync"
)

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

// Wait waits until either the event or context is done.
func (e *event) Wait(ctx context.Context) error {
	select {
	case <-e.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
