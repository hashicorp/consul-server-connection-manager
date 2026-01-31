// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

type Addr struct {
	// Something with IP + Port
	net.TCPAddr
}

func MakeAddr(ipStr string, port int) (Addr, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return Addr{}, fmt.Errorf("unable to parse ip: %q", ipStr)
	}
	return Addr{
		TCPAddr: net.TCPAddr{
			IP:   ip,
			Port: port,
		},
	}, nil
}

func (a Addr) String() string {
	return a.TCPAddr.String()
}

func (a Addr) Empty() bool {
	return len(a.IP) == 0
}

// addrSet represents a set of addresses. For each address it can track the
// last time the address was attempted and the last time the address was
// "updated" (inserted into the set).
type addrSet struct {
	sync.Mutex
	data  map[string]*addrWithStatus
	clock Clock
}

type addrWithStatus struct {
	Addr
	// lastAttempt is the last time the address was ever attempted. This field
	// is generally preserved by the addrSet.
	lastAttempt time.Time
	// lastUpdate is the last time the address was inserted to the addrSet. This
	// is set when the addrSet is first constructed, and when calling SetAddrs.
	lastUpdate time.Time
}

// unattempted returns whether the address has been attempted since the last update.
func (a *addrWithStatus) unattempted() bool {
	return a.lastAttempt.IsZero() || a.lastAttempt.Before(a.lastUpdate)
}

func newAddrSet(addrs ...Addr) *addrSet {
	a := &addrSet{
		data:  map[string]*addrWithStatus{},
		clock: &SystemClock{},
	}
	a.putNoLock(addrs...)
	return a
}

func (s *addrSet) putNoLock(addrs ...Addr) {
	for _, addr := range addrs {
		val, ok := s.data[addr.String()]
		if !ok {
			val = &addrWithStatus{Addr: addr}
		}
		// preserve lastAttempt
		val.lastUpdate = s.clock.Now()
		s.data[addr.String()] = val
	}
}

// SetAddrs replaces the existing addresses in this set with the given
// addresses. This preserves the lastAttempt timestamp for any of the given
// addresses that are already in the set. It sets lastUpdate to the current
// time for each address.
//
// After calling this function, each address is considered unattempted until
// the next call to SetAttemptTime.
func (s *addrSet) SetAddrs(addrs ...Addr) {
	s.Lock()
	defer s.Unlock()

	old := s.data

	s.data = map[string]*addrWithStatus{}
	s.putNoLock(addrs...)

	// preserve lastAttempt timestamps
	for _, oldVal := range old {
		if newVal, ok := s.data[oldVal.String()]; ok {
			newVal.lastAttempt = oldVal.lastAttempt
		}
	}
}

func (s *addrSet) AllAttempted() bool {
	if s == nil {
		// no addrs, so all attempted
		return true
	}
	s.Lock()
	defer s.Unlock()

	for _, a := range s.data {
		if a.unattempted() {
			return false
		}
	}
	return true
}

// Sorted returns an ordered list of addresses. This sorts first by moving
// unattempted addresses to the front, and then by the lastAttempt timestamp.
func (s *addrSet) Sorted() []Addr {
	if s == nil {
		return nil
	}
	s.Lock()
	defer s.Unlock()

	return s.sortNoLock()
}

func (s *addrSet) sortNoLock() []Addr {
	result := make([]Addr, 0, len(s.data))
	for _, a := range s.data {
		result = append(result, a.Addr)
	}

	sort.Slice(result, func(i, j int) bool {
		a := s.data[result[i].String()]
		b := s.data[result[j].String()]

		// Sort unattempted addresses to the front.
		if a.unattempted() != b.unattempted() {
			// If a != b then there are two cases:
			//   a=true   b=false  --> return true
			//   a=false  b=true   --> return false
			// Which simplifies to "return a"
			return a.unattempted()
		}
		// Otherwise, sort by lastAttempt.
		return a.lastAttempt.Before(b.lastAttempt)

	})
	return result
}

func (s *addrSet) SetAttemptTime(addr Addr) {
	s.Lock()
	defer s.Unlock()

	s.data[addr.String()].lastAttempt = s.clock.Now()
}

func (s *addrSet) String() string {
	if s == nil {
		return "<nil>"
	}
	s.Lock()
	defer s.Unlock()

	return fmt.Sprint(s.sortNoLock())
}
