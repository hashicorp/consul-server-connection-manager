// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

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

func TestAddrSetSort(t *testing.T) {
	addrs := []Addr{
		mustMakeAddr(t, "127.0.0.1", 1),
		mustMakeAddr(t, "127.0.0.1", 2),
		mustMakeAddr(t, "127.0.0.1", 3),
	}

	set := newAddrSet(addrs...)

	// match in any order.
	require.ElementsMatch(t, addrs, set.Sorted())

	// record the last attempt time.
	set.SetAttemptTime(addrs[1])
	sorted := set.Sorted()
	require.ElementsMatch(t, addrs, sorted)
	require.Equal(t, addrs[1], sorted[2]) // last attempted address at the end

	set.SetAttemptTime(addrs[0])
	require.Equal(t, []Addr{addrs[2], addrs[1], addrs[0]}, set.Sorted())

	set.SetAttemptTime(addrs[2])
	require.Equal(t, []Addr{addrs[1], addrs[0], addrs[2]}, set.Sorted())

	// Try some other order
	set.SetAttemptTime(addrs[0])
	set.SetAttemptTime(addrs[2])
	set.SetAttemptTime(addrs[1])
	require.Equal(t, []Addr{addrs[0], addrs[2], addrs[1]}, set.Sorted())

	// Set the lastUpdate time. The lastAttempt timestamp should be preserved,
	// and the should sort in the same order as prior to Update.
	set.SetAddrs(addrs...)
	require.Equal(t, []Addr{addrs[0], addrs[2], addrs[1]}, set.Sorted())
}

func TestAddrSetAllAttempted(t *testing.T) {
	var set *addrSet
	require.True(t, set.AllAttempted())

	addrs := []Addr{
		mustMakeAddr(t, "127.0.0.1", 1),
		mustMakeAddr(t, "127.0.0.1", 2),
		mustMakeAddr(t, "127.0.0.1", 3),
	}

	set = newAddrSet(addrs...)
	require.False(t, set.AllAttempted())
	set.SetAttemptTime(addrs[2])
	require.False(t, set.AllAttempted())
	set.SetAttemptTime(addrs[1])
	require.False(t, set.AllAttempted())
	set.SetAttemptTime(addrs[0])
	require.True(t, set.AllAttempted())
}
