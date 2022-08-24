package discovery

import (
	"fmt"
	"net"
	"sync"
)

type addrStatus bool

const (
	OK    addrStatus = true
	NotOK addrStatus = false
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

func (a Addr) Empty() bool {
	return len(a.TCPAddr.IP) == 0
}

type addrSet struct {
	sync.Mutex

	data map[string]addrWithStatus
}

type addrWithStatus struct {
	Addr
	addrStatus
}

func newAddrSet() *addrSet {
	return &addrSet{data: map[string]addrWithStatus{}}
}

func (s *addrSet) Put(status addrStatus, addrs ...Addr) {
	s.Lock()
	defer s.Unlock()

	for _, addr := range addrs {
		s.data[addr.String()] = addrWithStatus{addr, status}
	}
}

func (s *addrSet) Get(match addrStatus) []Addr {
	s.Lock()
	defer s.Unlock()

	var result []Addr
	for _, a := range s.data {
		if a.addrStatus == match {
			result = append(result, a.Addr)
		}
	}
	return result
}

func (s *addrSet) String() string {
	if s == nil {
		return "<nil>"
	}
	s.Lock()
	defer s.Unlock()

	return fmt.Sprint(s.data)
}
