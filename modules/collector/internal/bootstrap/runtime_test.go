package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCloseWaitsForWorkersBeforeClosingPersistence(t *testing.T) {
	runtime := newRuntime(context.Background(), nil)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	closedDatabase := make(chan struct{})
	runtime.closeDatabase = func() error { close(closedDatabase); return nil }
	require.True(t, runtime.launch(func() {
		close(started)
		<-runtime.ctx.Done()
		close(canceled)
		<-release
	}))
	<-started
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel its worker")
	}
	select {
	case <-closedDatabase:
		t.Fatal("persistence closed before worker exited")
	default:
	}
	require.False(t, runtime.launch(func() { t.Error("worker started after cancellation") }))
	close(release)
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not join its worker")
	}
	<-closedDatabase
	require.NoError(t, runtime.Close(), "cleanup is shared by failure and normal exit")
}
