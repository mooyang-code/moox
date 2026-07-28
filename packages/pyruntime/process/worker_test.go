package process

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupervisorRequiresFactory(t *testing.T) {
	s := NewSupervisor(func(context.Context) (Worker, error) { return nil, context.Canceled }, SupervisorConfig{})
	if _, err := s.Ensure(context.Background()); err == nil {
		t.Fatal("expected factory error")
	}
}

func TestStdioWorkerCloseReapsProcessAndIsIdempotent(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60")
	require.NoError(t, cmd.Start())
	w := &StdioWorker{cmd: cmd, state: StateReady}

	require.NoError(t, w.Close())
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)

	require.NoError(t, w.Close())
}

func TestStdioWorkerRunTimeoutReapsProcess(t *testing.T) {
	w, cmd := newSilentStdioWorker(t)
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := w.Run(ctx, RunRequest{RequestID: "timeout"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)
	require.NoError(t, w.Close())
}

func TestStdioWorkerLoadCancellationReapsProcess(t *testing.T) {
	w, cmd := newSilentStdioWorker(t)
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Load(ctx, LoadRequest{LogicalID: "cancel"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)
	require.NoError(t, w.Close())
}

func newSilentStdioWorker(t *testing.T) (*StdioWorker, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command("python3", "-c", "import time; time.sleep(60)")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	cfg := Config{TaskTimeout: time.Second}
	cfg.defaults()
	return &StdioWorker{
		cfg:   cfg,
		cmd:   cmd,
		in:    in,
		out:   out,
		state: StateReady,
	}, cmd
}
