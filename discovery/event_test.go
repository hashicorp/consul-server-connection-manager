package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvent(t *testing.T) {
	// check we can call these multiple times safely.
	e := newEvent()
	require.False(t, e.IsDone())
	require.False(t, e.IsDone())

	e.SetDone()
	require.True(t, e.IsDone())

	e.SetDone()
	require.True(t, e.IsDone())
}

func TestEventWait(t *testing.T) {
	e := newEvent()

	time.AfterFunc(50*time.Millisecond, e.SetDone)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	require.NoError(t, e.Wait(ctx))

	e = newEvent()
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)

	require.Error(t, e.Wait(ctx))
}
