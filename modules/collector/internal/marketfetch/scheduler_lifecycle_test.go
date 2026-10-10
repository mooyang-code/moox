package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedulerCloseCancelsAndJoinsDetachedDispatch(t *testing.T) {
	scheduler := &Scheduler{Lifetime: context.Background()}
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	require.True(t, scheduler.launch(func() {
		close(started)
		<-scheduler.lifetime().Done()
		close(canceled)
		<-release
	}))
	<-started
	closed := make(chan error, 1)
	go func() { closed <- scheduler.Close() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("detached dispatch was not canceled")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before dispatch exited")
	default:
	}
	require.False(t, scheduler.launch(func() { t.Error("dispatch started after Close") }))
	close(release)
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for dispatch")
	}
	require.NoError(t, scheduler.Close())
}
