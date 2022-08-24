package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMakeAddr(t *testing.T) {
	_, err := MakeAddr("", 0)
	require.Error(t, err)
	require.Equal(t, `unable to parse ip: ""`, err.Error())

	_, err = MakeAddr("asdf", 0)
	require.Error(t, err)
	require.Equal(t, `unable to parse ip: "asdf"`, err.Error())

	a, err := MakeAddr("127.0.0.1", 0)
	require.NoError(t, err)
	require.Equal(t, a.String(), "127.0.0.1:0")

	a, err = MakeAddr("127.0.0.1", 1234)
	require.NoError(t, err)
	require.Equal(t, a.String(), "127.0.0.1:1234")
}

func TestAddrEmpty(t *testing.T) {
	a := Addr{}
	require.True(t, a.Empty())

	a, err := MakeAddr("127.0.0.1", 0)
	require.NoError(t, err)
	require.False(t, a.Empty())
}

func TestAddrSet(t *testing.T) {
	addrs := []Addr{}

	for i := 0; i < 6; i++ {
		a, err := MakeAddr("127.0.0.1", i)
		require.NoError(t, err)
		addrs = append(addrs, a)
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
