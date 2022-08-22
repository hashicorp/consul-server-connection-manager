package discovery

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddrEmpty(t *testing.T) {
	a := Addr{}
	require.True(t, a.Empty())

	a = Addr{
		TCPAddr: net.TCPAddr{
			IP: net.ParseIP("127.0.0.1"),
		},
	}
	require.False(t, a.Empty())
}

func TestAddrSet(t *testing.T) {
	addrs := []Addr{}

	for i := 0; i < 6; i++ {
		addrs = append(addrs, Addr{
			TCPAddr: net.TCPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: i,
			},
		})
	}

	set := newAddrSet()

	require.Len(t, set.Get(OK), 0)
	require.Len(t, set.Get(NotOK), 0)

	set.Put(OK, addrs...)
	require.Len(t, set.Get(OK), len(addrs))
	require.Len(t, set.Get(NotOK), 0)

	set.Put(NotOK, addrs[:3]...)
	require.Len(t, set.Get(OK), 3)
	require.Len(t, set.Get(NotOK), 3)

	set.Put(NotOK, addrs...)
	require.Len(t, set.Get(OK), 0)
	require.Len(t, set.Get(NotOK), 6)
}
