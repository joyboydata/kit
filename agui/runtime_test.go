package agui

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitClosed fails the test unless ch closes before the timeout.
func waitClosed(t *testing.T, ch <-chan []Event) {
	t.Helper()
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel delivered a value on a backend-less stub")
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close")
	}
}

func TestNewRuntime(t *testing.T) {
	require.NotNil(t, NewRuntime())
}

// No backend ships in kit, so every I/O method fails loudly instead of
// pretending to persist or execute.
func TestRuntimeNoBackend(t *testing.T) {
	rt := NewRuntime()
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		th, err := rt.CreateThread(ctx)
		require.ErrorIs(t, err, errNoBackend)
		require.Equal(t, Thread{}, th)
	})
	t.Run("open", func(t *testing.T) {
		th, err := rt.OpenThread("th-1")
		require.ErrorIs(t, err, errNoBackend)
		require.Equal(t, Thread{}, th)
	})
	t.Run("resume", func(t *testing.T) {
		threads, err := rt.Resume(ctx)
		require.ErrorIs(t, err, errNoBackend)
		require.Nil(t, threads)
	})
	t.Run("active runs", func(t *testing.T) {
		events, err := rt.ListActiveRuns(ctx, "owner-1")
		require.ErrorIs(t, err, errNoBackend)
		require.Nil(t, events)
	})
}

func TestThreadID(t *testing.T) {
	require.Equal(t, "", Thread{}.ID())
	require.Equal(t, "th-1", Thread{id: "th-1"}.ID())
}

func TestThreadSendInputNoBackend(t *testing.T) {
	runID, err := Thread{id: "th-1"}.SendInput(context.Background(), RunInput{ThreadID: "th-1"})
	require.ErrorIs(t, err, errNoBackend)
	require.Equal(t, "", runID)
}

// The returned func is the caller-owned cancel: it closes the channel, and
// calling it twice is safe.
func TestSubscribeCloseOnCancel(t *testing.T) {
	ch, cancel := Thread{id: "th-1"}.Subscribe(context.Background(), 0)
	require.NotNil(t, ch)
	require.NotNil(t, cancel)

	select {
	case <-ch:
		t.Fatal("channel closed before cancel")
	default:
	}

	cancel()
	waitClosed(t, ch)
	require.NotPanics(t, cancel)
}

// Binding to ctx means ctx expiry terminates the stream the same way.
func TestSubscribeCloseOnCtxDone(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ch, cancel := Thread{id: "th-1"}.Subscribe(ctx, 0)
	defer cancel()

	stop()
	waitClosed(t, ch)
}

// A pre-cancelled ctx terminates immediately: no hang waiting for a backend.
func TestSubscribePreCancelledCtx(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stop()

	ch, cancel := Thread{id: "th-1"}.Subscribe(ctx, 0)
	defer cancel()
	waitClosed(t, ch)
}
