package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvent(t *testing.T) {
	e := newEvent()
	require.False(t, e.IsDone())
	require.False(t, e.IsDone())

	e.SetDone()
	require.True(t, e.IsDone())
	require.True(t, e.IsDone())

	e.SetDone()
	require.True(t, e.IsDone())
	require.True(t, e.IsDone())
}
