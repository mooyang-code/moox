package process

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupervisorCloseAndRunLoadedMany(t *testing.T) {
	w := &memoryWorker{}
	s := NewSupervisor(func(context.Context) (Worker, error) { return w, nil }, SupervisorConfig{})
	require.NoError(t, s.Close())

	result, err := s.RunLoadedMany(context.Background(), []LoadRequest{{LogicalID: "a"}, {LogicalID: "b"}}, RunRequest{RequestID: "r"})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Meta)
	assert.Equal(t, StateReady, s.State())
	require.NoError(t, s.Close())
	assert.Equal(t, StateStarting, s.State())
}

func TestSupervisorDefaultsApplied(t *testing.T) {
	s := NewSupervisor(func(context.Context) (Worker, error) {
		return &memoryWorker{}, nil
	}, SupervisorConfig{MaxRetries: -1})
	assert.Equal(t, 0, s.cfg.MaxRetries)
	assert.True(t, s.cfg.BackoffMin > 0)
	assert.True(t, s.cfg.BackoffMax > 0)
	assert.True(t, s.cfg.MaxConsecutiveFailures > 0)
}
