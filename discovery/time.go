// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import "time"

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	Sleep(d time.Duration)
}

type SystemClock struct{}

var _ Clock = (*SystemClock)(nil)

func (*SystemClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

func (*SystemClock) Now() time.Time {
	return time.Now()
}

func (*SystemClock) Sleep(d time.Duration) {
	time.Sleep(d)
}
