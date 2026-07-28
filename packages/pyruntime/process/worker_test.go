package process

import (
	"context"
	"os/exec"
	"testing"

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
